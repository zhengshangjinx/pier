package mcp

import "fmt"

// 七个工具的入参 schema，JSON Schema 2020-12。
//
// 手写而不是从结构体生成：给模型读的那几句说明（哪个必填、空着是什么意思）
// 本来就要一句句写，生成器省下的那点字远不抵它自己带来的规矩。schema 里写的
// 属性名与上面那些结构体的 json 标签必须对得上，由 TestSchemasMatchParams 守着。
//
// 每个都关掉 additionalProperties：客户端送进来的多余字段会被 tools/call
// 当场拒掉（见 call），把它写出来，那个拒绝才是可预期的，而不是「试了才知道」。
// 代价是模型偶尔会按自己的想当然多写一个字段——那时它收到一句明确的拒绝，
// 比我们猜它的意思然后动了整份清单安全得多。
const (
	listSchema = `{
  "type": "object",
  "description": "不需要参数。",
  "properties": {},
  "additionalProperties": false
}`

	statusSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "服务名，与 list_services 里那一列一样"}
  },
  "required": ["name"],
  "additionalProperties": false
}`

	logsSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "服务名"},
    "lines": {"type": "integer", "minimum": 1, "description": "要末尾多少行，默认 200"},
    "date": {"type": "string", "description": "哪一天，写成 2006-01-02 这样；不给就是它此刻正在写的那一份"}
  },
  "required": ["name"],
  "additionalProperties": false
}`

	waitSchema = `{
  "type": "object",
  "description": "等服务通过健康探针。没等到的服务会写清楚卡在哪一步，这一条不算调用失败。",
  "properties": {
    "names": {"type": "array", "items": {"type": "string"}, "description": "要等的服务名，可以一次给几个"},
    "group": {"type": "string", "description": "要等的那一组"},
    "timeout": {"type": "string", "description": "最多等多久，例如 30s、2m，也可以只写秒数；默认 3m"}
  },
  "additionalProperties": false
}`
)

// selSchema 是那三个批量动作的入参 schema，只有动词不同。
//
// 合成一份是因为它们三个的选择方式是同一件事（见 panel.Selection）：
// 分开写三份的话，「两个都不给就是全部」这句话迟早有一处漏掉，
// 而漏掉的那一个会让模型以为必须给点什么。
func selSchema(verb string) string {
	return fmt.Sprintf(`{
  "type": "object",
  "description": "names 与 group 只能给一个；两个都不给就是全部服务（标了「不参与全部启停」的除外）。",
  "properties": {
    "names": {"type": "array", "items": {"type": "string"}, "description": "要%s的服务名，可以一次给几个"},
    "group": {"type": "string", "description": "要%s的那一个分组"}
  },
  "additionalProperties": false
}`, verb, verb)
}
