// 查看页的少量交互：切换版本、恢复历史版本。
(function () {
  var sel = document.getElementById('ver-select');
  if (sel) {
    sel.addEventListener('change', function () {
      location.href = sel.dataset.base + '?v=' + encodeURIComponent(sel.value);
    });
  }

  var csrf = document.querySelector('meta[name="csrf"]');
  var status = document.getElementById('restore-status');
  document.querySelectorAll('[data-restore]').forEach(function (btn) {
    var armed = false;
    btn.addEventListener('click', function () {
      var v = btn.dataset.restore;
      if (!armed) {
        // 二次确认：第一次点击只改变按钮文字
        armed = true;
        btn.textContent = '确认恢复 v' + v;
        btn.classList.add('primary');
        setTimeout(function () {
          if (armed) { armed = false; btn.textContent = '恢复为新版本'; btn.classList.remove('primary'); }
        }, 4000);
        return;
      }
      armed = false;
      btn.disabled = true;
      btn.textContent = '正在恢复…';
      fetch('/api/admin/artifacts/' + encodeURIComponent(btn.dataset.id) + '/rollback', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf ? csrf.content : '' },
        body: JSON.stringify({ version: Number(v) })
      }).then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, j: j }; });
      }).then(function (res) {
        if (!res.ok) throw new Error((res.j.error && res.j.error.message) || '恢复失败');
        if (status) status.textContent = '已用 v' + v + ' 的内容生成新版本 v' + res.j.version + '。';
        setTimeout(function () { location.reload(); }, 700);
      }).catch(function (e) {
        btn.disabled = false;
        btn.textContent = '恢复为新版本';
        btn.classList.remove('primary');
        if (status) status.textContent = e.message;
      });
    });
  });
})();
