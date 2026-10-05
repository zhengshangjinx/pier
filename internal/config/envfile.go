package config

import (
	"fmt"
	"os"
	"strings"
)

// EnvFileName 是服务目录下会被自动注入的那份文件。
//
// 只认这一份，不去猜 .env.local / .env.development 该取哪个：那几个后缀是各个框架
// 自成一套的地方（Vite 与 Next 的优先级正好相反），Pier 替用户决定只会在两边打架时
// 更难解释。要那几种，就在这一份里写，或者用清单里的 env。
const EnvFileName = ".env"

// EnvKV 是一条环境变量，带着它在文件里（或清单里）的位置。
//
// 用具名类型而不是 map：合成环境时要按确定顺序合并（map 的遍历顺序随机，
// 同一份清单每次启动会得到顺序不同的环境，日志就没法比对了）。
type EnvKV struct {
	Key   string
	Value string
}

// ParseEnvFile 解析一份 .env。
//
// 认的是 dotenv 的通行做法，一样不多认：
//   - 空行与 # 开头的整行注释跳过；
//   - 第一个 = 分左右，两端空白去掉；键可以带 export 前缀（从 shell 脚本里抄来的
//     .env 常常带着它）；
//   - 值两端成对的引号去掉一层，引号里的内容原样保留——不做转义。.env 不是脚本，
//     里面的 \n 就该是两个字符，转义规则各家实现本来也各不相同；
//   - 同名键以最后一条为准，与 dotenv 一致。
//
// 两处刻意不做的：**值里行尾的 # 不当注释**（密码里带 # 是常事，切掉一次就再也登不上，
// 而报错都不会有），**不展开 ${}**（那是清单里才有的写法，见 ResolveEnvLayer；
// 这个文件别的工具也会读，改它的内容等于替别人做主）。
//
// 看不懂的行直接报错，不静默跳过：少一个变量，服务起来时是另一副样子，
// 而没人会想到去 .env 里找一个少掉的引号。
func ParseEnvFile(text string) ([]EnvKV, error) {
	// UTF-8 BOM：Windows 上记事本存的文件常带着它，不去掉的话第一个键名前面会多出三个字节，
	// 于是那个变量永远注入不进去，而文件看上去完全正常。
	text = strings.TrimPrefix(text, "\uFEFF")

	var out []EnvKV
	at := make(map[string]int)
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s 第 %d 行看不懂（要写成 键=值）：%s", EnvFileName, i+1, line)
		}
		key = strings.TrimSpace(key)
		if !ValidEnvName(key) {
			return nil, fmt.Errorf("%s 第 %d 行的变量名不合法：%s", EnvFileName, i+1, key)
		}
		v, err := unquoteValue(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%s 第 %d 行的 %s：%w", EnvFileName, i+1, key, err)
		}
		// 同名以最后一条为准，但位置留在它第一次出现的地方：
		// 一个值被写了两遍是常见的（改了一半又补一行），哪一条算数不该由读的人去猜。
		if j, dup := at[key]; dup {
			out[j].Value = v
			continue
		}
		at[key] = len(out)
		out = append(out, EnvKV{Key: key, Value: v})
	}
	return out, nil
}

// LoadEnvFile 读一份 .env。
//
// 文件不存在不算错误——绝大多数服务的目录里没有它，那是常态不是问题。
// 读得动但解析不了才是错误：那说明用户写了这份文件而它没生效。
func LoadEnvFile(path string) ([]EnvKV, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s 失败：%w", path, err)
	}
	return ParseEnvFile(string(raw))
}

// unquoteValue 去掉值两端成对的引号；没引号就原样返回。
//
// 与 probe.go 里那个 unquote 是两件事：那个是读 YAML 时随手去引号，认不出来就原样返回；
// 这里是读用户手写的 .env，引号没配对要报错——那个值多半是写错了，而不是「本来就带引号」。
func unquoteValue(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if q := s[0]; q == '"' || q == '\'' {
		if len(s) < 2 || s[len(s)-1] != q {
			return "", fmt.Errorf("引号没有配对：%s", s)
		}
		return s[1 : len(s)-1], nil
	}
	return s, nil
}

// ValidEnvName 报告一个名字能不能当环境变量名（[A-Za-z_][A-Za-z0-9_]*）。
//
// 卡得这么死是因为它同时也是 ${} 里认的名字：放开来（允许短横线、允许中文）
// 会让 ${A-B} 这种写法在清单里看着像减法、在别处又像变量，读的人得先猜是哪一种。
func ValidEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
