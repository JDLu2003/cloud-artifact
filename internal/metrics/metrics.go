// Package metrics 采集进程、容器、磁盘和请求指标，供管理后台展示。
package metrics

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const slots = 60 // 保留最近 60 分钟

type minute struct {
	Minute   int64     `json:"minute"` // unix 分钟
	Requests int64     `json:"requests"`
	Errors   int64     `json:"errors"` // 5xx
	Client   int64     `json:"client"` // 4xx
	LatSum   float64   `json:"-"`
	Buckets  [10]int64 `json:"-"`
	P95      float64   `json:"p95_ms"`
	Avg      float64   `json:"avg_ms"`
	CPU      float64   `json:"cpu"` // 进程 CPU 占用（%，单核为 100）
	RSS      int64     `json:"rss"` // 字节
}

var bucketBounds = [...]float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 1e18}

type Collector struct {
	mu      sync.Mutex
	ring    [slots]minute
	started time.Time
	lastCPU float64
	lastAt  time.Time
	cpuNow  float64
}

func New() *Collector {
	c := &Collector{started: time.Now()}
	c.lastCPU, c.lastAt = procCPUSeconds(), time.Now()
	go c.loop()
	return c
}

func (c *Collector) slot(now time.Time) *minute {
	m := now.Unix() / 60
	s := &c.ring[m%slots]
	if s.Minute != m {
		*s = minute{Minute: m}
	}
	return s
}

// Observe 记录一次请求。
func (c *Collector) Observe(status int, d time.Duration) {
	ms := float64(d.Microseconds()) / 1000
	c.mu.Lock()
	s := c.slot(time.Now())
	s.Requests++
	if status >= 500 {
		s.Errors++
	} else if status >= 400 {
		s.Client++
	}
	s.LatSum += ms
	for i, b := range bucketBounds {
		if ms <= b {
			s.Buckets[i]++
			break
		}
	}
	c.mu.Unlock()
}

func (c *Collector) loop() {
	t := time.NewTicker(10 * time.Second)
	for range t.C {
		cpu := procCPUSeconds()
		now := time.Now()
		pct := 0.0
		if el := now.Sub(c.lastAt).Seconds(); el > 0 {
			pct = (cpu - c.lastCPU) / el * 100
		}
		c.lastCPU, c.lastAt = cpu, now
		rss := procRSS()
		c.mu.Lock()
		c.cpuNow = pct
		s := c.slot(now)
		if pct > s.CPU {
			s.CPU = pct
		}
		if rss > s.RSS {
			s.RSS = rss
		}
		c.mu.Unlock()
	}
}

// Series 返回最近 60 分钟的指标，按时间升序，缺失的分钟补零。
func (c *Collector) Series() []minute {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().Unix() / 60
	out := make([]minute, 0, slots)
	for m := now - slots + 1; m <= now; m++ {
		s := c.ring[m%slots]
		if s.Minute != m {
			s = minute{Minute: m}
		}
		if s.Requests > 0 {
			s.Avg = s.LatSum / float64(s.Requests)
			target := int64(float64(s.Requests)*0.95 + 0.5)
			var acc int64
			for i, n := range s.Buckets {
				acc += n
				if acc >= target {
					s.P95 = bucketBounds[i]
					if s.P95 > 1e17 {
						s.P95 = 5000
					}
					break
				}
			}
		}
		out = append(out, s)
	}
	return out
}

type System struct {
	Uptime       int64   `json:"uptime"`
	GoVersion    string  `json:"go_version"`
	NumCPU       int     `json:"num_cpu"`
	Goroutines   int     `json:"goroutines"`
	HeapAlloc    uint64  `json:"heap_alloc"`
	HeapSys      uint64  `json:"heap_sys"`
	NumGC        uint32  `json:"num_gc"`
	RSS          int64   `json:"rss"`
	CPU          float64 `json:"cpu"`
	MemLimit     int64   `json:"mem_limit"`   // cgroup 内存上限，0 表示未限制
	MemCurrent   int64   `json:"mem_current"` // cgroup 当前用量
	HostMemTotal int64   `json:"host_mem_total"`
	HostMemAvail int64   `json:"host_mem_avail"`
	Load1        float64 `json:"load1"`
	Load5        float64 `json:"load5"`
	DiskTotal    int64   `json:"disk_total"`
	DiskFree     int64   `json:"disk_free"`
}

func (c *Collector) System(dataDir string) System {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	c.mu.Lock()
	cpu := c.cpuNow
	c.mu.Unlock()
	s := System{
		Uptime:     int64(time.Since(c.started).Seconds()),
		GoVersion:  runtime.Version(),
		NumCPU:     runtime.NumCPU(),
		Goroutines: runtime.NumGoroutine(),
		HeapAlloc:  ms.HeapAlloc,
		HeapSys:    ms.HeapSys,
		NumGC:      ms.NumGC,
		RSS:        procRSS(),
		CPU:        cpu,
	}
	s.MemLimit = readInt("/sys/fs/cgroup/memory.max")
	s.MemCurrent = readInt("/sys/fs/cgroup/memory.current")
	if s.MemLimit > 1<<50 {
		s.MemLimit = 0
	}
	s.HostMemTotal, s.HostMemAvail = meminfo()
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 2 {
			s.Load1, _ = strconv.ParseFloat(f[0], 64)
			s.Load5, _ = strconv.ParseFloat(f[1], 64)
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &st); err == nil {
		s.DiskTotal = int64(st.Blocks) * int64(st.Bsize)
		s.DiskFree = int64(st.Bavail) * int64(st.Bsize)
	}
	return s
}

func readInt(p string) int64 {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	v := strings.TrimSpace(string(b))
	if v == "max" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func meminfo() (total, avail int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(fs[1], 10, 64)
		switch fs[0] {
		case "MemTotal:":
			total = n * 1024
		case "MemAvailable:":
			avail = n * 1024
		}
	}
	return
}

func procRSS() int64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return int64(ms.Sys)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				n, _ := strconv.ParseInt(fs[1], 10, 64)
				return n * 1024
			}
		}
	}
	return 0
}

// procCPUSeconds 读取进程累计 CPU 时间（用户态 + 内核态）。
func procCPUSeconds() float64 {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	// 第二个字段是带括号的进程名，可能含空格，从最后一个 ) 之后开始解析
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 13 {
		return 0
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return (ut + st) / 100 // 时钟频率按 100Hz
}
