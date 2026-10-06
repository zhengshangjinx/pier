package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// 接口说明文档。
//
// 写在这里而不是生成出来：生成的文档只会重复一遍字段名，而调用的人真正要看的
// 是「这个接口什么时候用、失败是什么样、为什么是 202」。这两件事机器写不出来。
//
// 代价是它会和代码走散，所以 routes() 是唯一的事实源，另有一条测试核对
// 文档里写到的路径集合与它逐个对得上——加了接口忘了改这里，测试就会红。
const openAPISpec = `{
  "openapi": "3.1.0",
  "info": {
    "title": "Pier 本地接口",
    "version": "1.0.0",
    "description": "启停本机上的服务、读服务状态与日志。只监听回环地址；除 /health 与 /openapi.json 外，每个接口都要带上 Authorization: Bearer <令牌>。令牌存在 ~/.pier/settings.json 的 apiToken 里，命令 ` + "`pier api --show-token`" + ` 可以直接打印出来。\n\n启停都是异步入队：接口返回 202 只表示动作已经排进队列，服务真正起来要等编译、拉起、健康检查走完。想等结果就轮询 GET /api/services/{name}。"
  },
  "servers": [{ "url": "http://127.0.0.1:7717" }],
  "paths": {
    "/health": {
      "get": {
        "summary": "探活",
        "description": "不需要令牌。脚本在拿到令牌之前用它确认 Pier 在不在。",
        "security": [],
        "responses": {
          "200": {
            "description": "服务在跑",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Health" } } }
          }
        }
      }
    },
    "/openapi.json": {
      "get": {
        "summary": "这份文档",
        "security": [],
        "responses": { "200": { "description": "OpenAPI 3.1 文档" } }
      }
    },
    "/api/state": {
      "get": {
        "summary": "完整快照",
        "description": "与图形界面每次刷新拿到的是同一份东西：分组、服务、各自的占用、排队中的动作、失败原因。字段见 /api/services 里的说明，这里多出分组、合计占用与面板自身占用。",
        "responses": {
          "200": {
            "description": "快照",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/StateResponse" } } }
          },
          "401": { "$ref": "#/components/responses/Unauthorized" }
        }
      }
    },
    "/api/services": {
      "get": {
        "summary": "列出全部服务",
        "responses": {
          "200": {
            "description": "服务列表，顺序与界面侧栏一致",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ServicesResponse" } } }
          },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "503": { "$ref": "#/components/responses/NoConfig" }
        }
      }
    },
    "/api/services/start": {
      "post": {
        "summary": "启动一批服务",
        "description": "一次动一批：请求体是 {\"names\":[\"api\",\"web\"]} 或 {\"group\":\"前端\"}，两个字段只能给一个。请求体留空（或发一个 {}）就是「全部」——与界面上那颗「全部启动」、命令行不带名字的 pier up 是同一份名单，标了 manual 的服务不在其中。点名或按分组挑出来的会连同前置一起起，msg 里会写明多带了哪几个。",
        "requestBody": {
          "required": false,
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Selection" } } }
        },
        "responses": {
          "202": { "$ref": "#/components/responses/Accepted" },
          "400": { "$ref": "#/components/responses/BadSelection" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" },
          "409": { "$ref": "#/components/responses/Conflict" },
          "503": { "$ref": "#/components/responses/NoConfig" }
        }
      }
    },
    "/api/services/stop": {
      "post": {
        "summary": "停止一批服务",
        "description": "与启动同一份选择，顺序反着来（先停没被依赖的）。任何阶段都能停：排队中直接撤销，编译中连同整组结束进程。",
        "requestBody": {
          "required": false,
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Selection" } } }
        },
        "responses": {
          "202": { "$ref": "#/components/responses/Accepted" },
          "400": { "$ref": "#/components/responses/BadSelection" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" },
          "409": { "$ref": "#/components/responses/Conflict" },
          "503": { "$ref": "#/components/responses/NoConfig" }
        }
      }
    },
    "/api/services/wait": {
      "post": {
        "summary": "等一批服务就绪",
        "description": "等选择出来的那些通过健康探针，返回每一个的结果。这是「启动之后等它真的能用」那一步：POST …/start 立刻回 202，接下来靠这条等，不必自己轮询 /api/state——轮询只看得出在不在跑，看不出探针通没通。还在编译、拉起中的服务会先等它出结果再探。\n\n注意 ok 的含义与别处不同：这里说的是「全都就绪了没有」，所以全部就绪是 200 + ok=true，有没等到的是 200 + ok=false（不是错误，是一个答案）。探针没通的那几条各自带上 why：no_probe（清单里没写 health，等不到这个信号）、not_running（没在跑，先去起它）、timeout（等满了窗口）。",
        "parameters": [
          {
            "name": "timeout", "in": "query", "required": false,
            "schema": { "type": "string", "default": "180s" },
            "description": "等多久，如 30s、2m，光写数字按秒算。不填用健康探针的默认窗口 180 秒。这是整段共用的窗口（先等它起起来，再等探针通）。"
          }
        ],
        "requestBody": {
          "required": false,
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Selection" } } }
        },
        "responses": {
          "200": {
            "description": "等完了，结果在 wait 里；ok 为假表示有没等到的",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/WaitResponse" } } }
          },
          "400": { "description": "请求体不是 JSON 对象，或 timeout 不是时长" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" },
          "503": { "$ref": "#/components/responses/NoConfig" }
        }
      }
    },
    "/api/services/{name}": {
      "get": {
        "summary": "看一个服务",
        "parameters": [{ "$ref": "#/components/parameters/Name" }],
        "responses": {
          "200": {
            "description": "这一个服务",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ServiceResponse" } } }
          },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      }
    },
    "/api/services/{name}/start": {
      "post": {
        "summary": "启动一个服务",
        "description": "配了 depends_on 的话，前置的那些会一并排进队列，顺序在前。已经在跑的前置会跳过。",
        "parameters": [{ "$ref": "#/components/parameters/Name" }],
        "responses": {
          "202": { "$ref": "#/components/responses/Accepted" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" },
          "409": { "$ref": "#/components/responses/Conflict" }
        }
      }
    },
    "/api/services/{name}/stop": {
      "post": {
        "summary": "停止一个服务",
        "description": "任何阶段都能停：排队中直接撤销，编译中连同整组结束进程，等待就绪中取消等待并停进程。",
        "parameters": [{ "$ref": "#/components/parameters/Name" }],
        "responses": {
          "202": { "$ref": "#/components/responses/Accepted" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" },
          "409": { "$ref": "#/components/responses/Conflict" }
        }
      }
    },
    "/api/services/{name}/restart": {
      "post": {
        "summary": "重启一个服务",
        "description": "先停后起，走的是同一个队列。",
        "parameters": [{ "$ref": "#/components/parameters/Name" }],
        "responses": {
          "202": { "$ref": "#/components/responses/Accepted" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" },
          "409": { "$ref": "#/components/responses/Conflict" }
        }
      }
    },
    "/api/services/{name}/logs": {
      "get": {
        "summary": "读日志",
        "description": "一次最多给 2000 行、256 KB，从文件末尾往前取。带上上一次返回的 offset 作为 since，就只拿新增的那一段——日志抽屉每秒拉一次，整读一个几十兆的 Maven 输出会把磁盘和内存都拖垮。",
        "parameters": [
          { "$ref": "#/components/parameters/Name" },
          {
            "name": "date", "in": "query", "required": false,
            "schema": { "type": "string" },
            "description": "要读哪一天，格式 2026-10-03。不填就读这个服务最新的一份。跨了零点还在跑的服务写的一直是启动那天的文件，所以这里读的是「启动那一天」而不是「今天」。"
          },
          {
            "name": "since", "in": "query", "required": false,
            "schema": { "type": "integer", "minimum": 0 },
            "description": "已经读到的字节数。返回里的 offset 就是下一次该传的值。"
          }
        ],
        "responses": {
          "200": {
            "description": "日志尾部",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/LogResponse" } } }
          },
          "400": { "description": "since 不是整数" },
          "401": { "$ref": "#/components/responses/Unauthorized" },
          "404": { "$ref": "#/components/responses/NotFound" },
          "500": { "description": "日志文件读不出来" }
        }
      }
    }
  },
  "components": {
    "securitySchemes": {
      "bearer": { "type": "http", "scheme": "bearer" }
    },
    "parameters": {
      "Name": {
        "name": "name", "in": "path", "required": true,
        "schema": { "type": "string" },
        "description": "服务名，与清单里写的一致，可能含中文，需要做 URL 编码。"
      }
    },
    "responses": {
      "Unauthorized": {
        "description": "缺少或错误的访问令牌",
        "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Failure" } } }
      },
      "NotFound": {
        "description": "清单里没有这个服务或分组。msg 里会列出可用的名字——分组写错时还列出有哪些分组。",
        "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Failure" } } }
      },
      "BadSelection": {
        "description": "选择本身写得不对：names 与 group 都给了、names 里有个空名字，或者键名不是这两个。",
        "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Failure" } } }
      },
      "Conflict": {
        "description": "服务在清单里，但此刻不能动它：已经在跑、正在编译、或者正卡在别的动作里。原因在 msg 里。",
        "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Failure" } } }
      },
      "NoConfig": {
        "description": "清单没加载成功，此时没有服务可列。原因在 msg 里。",
        "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Failure" } } }
      },
      "Accepted": {
        "description": "动作已排进队列。注意这不等于服务已经起来。",
        "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Success" } } }
      }
    },
    "schemas": {
      "Failure": {
        "type": "object",
        "required": ["ok", "msg"],
        "properties": {
          "ok": { "type": "boolean", "const": false },
          "msg": { "type": "string", "description": "给人看的中文原因，可以直接显示。" }
        }
      },
      "Success": {
        "type": "object",
        "required": ["ok", "msg"],
        "properties": {
          "ok": { "type": "boolean", "const": true },
          "msg": { "type": "string" }
        }
      },
      "Health": {
        "type": "object",
        "required": ["ok", "app"],
        "properties": {
          "ok": { "type": "boolean", "const": true },
          "app": { "type": "string", "const": "pier" },
          "msg": { "type": "string", "description": "当前这份清单是从哪来的。" }
        }
      },
      "Service": {
        "type": "object",
        "required": ["name"],
        "description": "一个服务此刻的样子，与图形界面每次刷新拿到的是同一个结构。它比脚本通常需要的信息多得多——界面要的那一份原样给出来了，不多做一份「接口专用的精简版」，那种版本迟早和界面看到的不一样。",
        "properties": {
          "name": { "type": "string" },
          "kind": { "type": "string", "description": "服务类型：go / java / node / python / shell，空表示按目录内容自动识别。" },
          "dir": { "type": "string", "description": "服务工作目录的绝对路径。" },
          "port": { "type": "integer", "description": "清单里写的端口，0 表示没写。" },
          "runPort": { "type": "integer", "description": "这次运行实际用的端口，没在跑或没配端口时为 0。绝大多数时候与 port 相同，只有「换一个端口起」那一次不一样。" },
          "portNote": { "type": "string", "description": "解释 runPort 为什么和 port 对不上，两者一致时为空。" },
          "group": { "type": "string", "description": "分组名，空归入「未分组」。" },
          "statusKey": {
            "type": "string",
            "enum": ["running", "starting", "external", "stale", "stopped"],
            "description": "归纳出来的状态。与 portOpen 可以不一致：「外部运行」（external）就是端口被 Pier 之外的进程占着。判断脚本该走哪条路用它，别用中文文案。"
          },
          "statusText": { "type": "string", "description": "statusKey 的中文，界面与命令行显示的就是它。" },
          "portText": { "type": "string", "description": "端口那一格的文案，带「是否真的在听」的标记，如 47811 ✓、47811 (被占)、-。" },
          "pid": { "type": "integer", "description": "Pier 记录在案的进程号，未运行时为 0。" },
          "uptime": { "type": "string", "description": "运行时长，未运行时是 \"-\"。" },
          "health": { "type": "string", "description": "清单里写的健康探针地址，空表示没配。" },
          "healthy": { "type": "boolean", "description": "探针此刻通不通。没配探针时为假，看 hasHealth 区分。" },
          "hasHealth": { "type": "boolean", "description": "配了探针没有。" },
          "probeExpired": { "type": "boolean", "description": "等满健康探测窗口仍未通过。注意这时 statusKey 已经退回 running：服务在好好跑着，只是探不进去。" },
          "note": { "type": "string", "description": "后端补的一句说明，解释为什么是现在这个状态。" },
          "running": { "type": "boolean", "description": "进程在不在，由 Pier 记录在案的 PID/PGID 认领——端口常握在子进程手里，只比 PID 认不出来。" },
          "portOpen": { "type": "boolean" },
          "stale": { "type": "boolean", "description": "状态文件里有记录但进程已经不在了。" },
          "logPath": { "type": "string" },
          "userNote": { "type": "string", "description": "清单里手写的备注，原样给你，Pier 不解释它的内容。" },
          "run": { "type": "string" },
          "build": { "type": "string" },
          "module": { "type": "string" },
          "script": { "type": "string" },
          "env": { "type": "object", "additionalProperties": { "type": "string" } },
          "toolchain": { "type": "object", "additionalProperties": { "type": "string" }, "description": "服务上指定的 SDK，按类别，值是 SDK 路径。" },
          "runtimes": { "type": "array", "items": { "type": "object" }, "description": "启动时实际会用的 SDK 与选择依据，与真正启动走的是同一份解析结果。" },
          "dependsOn": { "type": "array", "items": { "type": "string" }, "description": "前置服务。只写名字的（\"mysql\"）只排启动顺序；带 \":healthy\" 的（\"mysql:healthy\"）还会在启动之前等它就绪，等不到照样起，结果写在 depNote 里。" },
          "restart": { "type": "string", "description": "重启策略，目前只有 on-failure：不是 Pier 叫它停的，就再起一次。留空不重启。" },
          "manual": { "type": "boolean", "description": "为真表示它不参与全部启停：「全部启动 / 全部停止」（界面上的按钮、pier up 与 pier down 不带名字时的那个「全部」）都会跳开它，点名时照做。" },
          "watch": { "type": "array", "items": { "type": "string" }, "description": "这个服务正在盯的那些文件模式（相对服务目录），改了这些文件就会重跑一次。清单里写 watch: true 时这里给的是按类型展开后的那一份——界面要说的是「它盯着什么」。空表示没配监视。" },
          "watchAuto": { "type": "boolean", "description": "为真表示上面那串模式是按服务类型给的默认，不是清单里点名的；为假而 watch 非空表示清单里就写了这些。" },
          "restartNote": { "type": "string", "description": "最近几次自动重启的说明，没发生过则为空。" },
          "depNote": { "type": "string", "description": "最近一次启动没等到的前置，如「没有等到 mysql 就绪」；等到了或没有要等的则为空。只在被启动的那一次记得住，重启 Pier 之后不再有。" },
          "editable": { "type": "boolean", "description": "为真表示这条定义在 Pier 自己的数据文件里，能改也能删；命令行指定 YAML 清单时为假。" },
          "occupant": { "type": "object", "description": "占着该端口的进程。端口开着又不是 Pier 起的时，直接说出「被谁占着」。字段见界面里的端口占用详情。", "additionalProperties": true },
          "op": { "type": "string", "description": "正在进行的动作文案，如「排队启动」「启动中」，空表示空闲。" },
          "opErr": { "type": "string", "description": "上一次动作失败的原因。" },
          "opKind": { "type": "string", "description": "进行中动作的类别：start / stop / restart。" },
          "usage": {
            "type": "object",
            "description": "这个服务整棵进程树的占用，未运行时都是 0。",
            "properties": {
              "cpu": { "type": "number" },
              "memBytes": { "type": "integer" },
              "procs": { "type": "integer" }
            },
            "additionalProperties": true
          },
          "diag": {
            "type": "object",
            "description": "从日志尾部读出来的「一句原因 + 一句下一步」，只在出事时有：启动失败（opErr 非空）或进程不见了（stale）。认不出来时为 null——宁可不说，也不能猜。",
            "properties": {
              "reason": { "type": "string", "description": "一句原因，如「端口被占着」。" },
              "next": { "type": "string", "description": "一句下一步，如「看是谁占的：pier ports」。" },
              "line": { "type": "string", "description": "命中的那行原文。端口号、模块名这些字只在它里面，而它们才是「该去改哪一处」的答案。" }
            },
            "additionalProperties": true
          }
        }
      },
      "Selection": {
        "type": "object",
        "description": "要动哪一批服务。留空表示「全部」。",
        "properties": {
          "names": {
            "type": "array", "items": { "type": "string" },
            "description": "点名的一批服务。标了 manual 的点了名照样动。"
          },
          "group": {
            "type": "string",
            "description": "按分组挑。没写 group 的服务归在「未分组」下。"
          }
        },
        "additionalProperties": false
      },
      "WaitResponse": {
        "type": "object",
        "required": ["ok", "wait"],
        "properties": {
          "ok": { "type": "boolean", "description": "是不是全都就绪了。" },
          "msg": { "type": "string", "description": "一句总结：全部就绪时是「N 个服务已就绪」，有没等到的会列出来并各带一句为什么。" },
          "wait": {
            "type": "array",
            "items": {
              "type": "object",
              "required": ["name", "ready"],
              "properties": {
                "name": { "type": "string" },
                "ready": { "type": "boolean" },
                "why": {
                  "type": "string",
                  "enum": ["no_probe", "not_running", "timeout"],
                  "description": "没就绪是哪一种，三种的下一步动作各不相同。就绪时没有这个字段。"
                },
                "probe": { "type": "string", "description": "这次实际探的地址。换过端口起的那次与清单里写的不是一个。" }
              }
            }
          }
        }
      },
      "ServicesResponse": {
        "type": "object",
        "required": ["ok", "services"],
        "properties": {
          "ok": { "type": "boolean", "const": true },
          "services": { "type": "array", "items": { "$ref": "#/components/schemas/Service" } }
        }
      },
      "ServiceResponse": {
        "type": "object",
        "required": ["ok", "service"],
        "properties": {
          "ok": { "type": "boolean", "const": true },
          "service": { "$ref": "#/components/schemas/Service" }
        }
      },
      "StateResponse": {
        "type": "object",
        "required": ["ok", "state"],
        "properties": {
          "ok": { "type": "boolean" },
          "msg": { "type": "string", "description": "ok 为假时是清单加载失败的原因。" },
          "state": {
            "type": "object",
            "description": "与图形界面每次刷新拿到的是同一份。",
            "properties": {
              "ok": { "type": "boolean" },
              "error": { "type": "string" },
              "configPath": { "type": "string" },
              "configDir": { "type": "string" },
              "configSource": { "type": "string" },
              "readOnly": { "type": "boolean", "description": "为真表示这份清单是命令行指定的 YAML，只能读。" },
              "groups": { "type": "array", "items": { "type": "object" } },
              "busyCount": { "type": "integer" },
              "services": { "type": "array", "items": { "$ref": "#/components/schemas/Service" } },
              "ungroupedName": { "type": "string" },
              "usage": { "type": "object", "description": "全部服务的 CPU / 内存合计。" },
              "self": { "type": "object", "description": "Pier 面板自身的占用。" },
              "metricsError": { "type": "string", "description": "非空表示这次采样没取到数，此时上面的用量都是 0。界面靠它把「读不到」和「什么都不占」分开。" }
            },
            "additionalProperties": true
          }
        }
      },
      "LogResponse": {
        "type": "object",
        "required": ["ok", "log"],
        "properties": {
          "ok": { "type": "boolean" },
          "log": {
            "type": "object",
            "required": ["text", "offset"],
            "properties": {
              "ok": { "type": "boolean" },
              "path": { "type": "string" },
              "text": { "type": "string" },
              "truncated": { "type": "boolean", "description": "为真表示头部被截掉了，text 不是这个文件的开头。" },
              "date": { "type": "string", "description": "这次真正读到的那一天。" },
              "dates": { "type": "array", "items": { "type": "string" }, "description": "这个服务有日志的那些天，从新到旧。" },
              "offset": { "type": "integer", "description": "已经读到的字节数，下一次作为 since 传回来。" },
              "reset": { "type": "boolean", "description": "为真表示 text 是整段内容，要整个替换掉手上的，而不是往后接。换了一天、文件被清过、同一天里又重启过一次，都会是整段。" }
            },
            "additionalProperties": true
          }
        }
      }
    }
  },
  "security": [{ "bearer": [] }]
}`

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	// 原样吐出去，不解析再序列化：这份文档里那句 "version" 是接口自己的版本，
	// 过一遍结构体会把它和各种别的东西搅在一起，没必要。
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(openAPISpec)))
	_, _ = w.Write([]byte(openAPISpec))
}

// specPaths 把文档里写到的路径与方法解出来，给测试核对用。
func specPaths() (map[string]map[string]bool, error) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(openAPISpec), &doc); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]bool, len(doc.Paths))
	for p, ops := range doc.Paths {
		out[p] = map[string]bool{}
		for m := range ops {
			out[p][m] = true
		}
	}
	return out, nil
}
