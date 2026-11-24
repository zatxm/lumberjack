# lumberjack

将日志写入滚动文件，支持按大小或按本地日期轮转。需要 Go 1.27.4 或更高版本。

```sh
go get github.com/zatxm/lumberjack/v26
```

```go
package main

import (
	"log"

	"github.com/zatxm/lumberjack/v26"
)

func main() {
	writer := &lumberjack.Logger{
		Filename:        "logs/app.log",
		PatternFileName: "logs/app-%Y-%m-%d.log",
		MaxBackups:      7,
		Compress:        true,
	}
	defer writer.Close()
	log.New(writer, "", log.LstdFlags).Println("服务已启动")
}
```

此配置将日志写入 `logs/app.log`。跨日后的第一条日志触发轮转，旧文件以所属日期命名。进程重启时，已有日志的最后修改时间决定其所属日期。省略 `PatternFileName` 可改用 `MaxSize` 按大小轮转，默认 100 MiB。

`PatternFileName` 支持日期、时间、周数和时区指令，以及 `%%`、`%-d`、`%#d` 等写法。相对路径以进程工作目录为准。清理与压缩只扫描活动日志所在目录，因此需要自动管理的备份应放在同一目录。对于周数等无法直接解析出完整日期的格式，清理时会用文件修改时间核对名称。

同一活动文件应由一个 Logger 管理。手动调用 `Rotate()` 会生成带毫秒时间戳的备份。遇到同名备份时会另取时间戳名称。`Close()` 会等待已安排的后台清理和压缩结束，并返回期间遇到的首个错误。
