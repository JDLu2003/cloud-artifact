// Package assets 打包随二进制分发的静态资源：技能包、管理后台、查看页模板和第三方库。
package assets

import "embed"

//go:embed skill web
var FS embed.FS
