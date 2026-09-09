文本对话（OpenAI）
支持两种 OpenAI 协议：经典的 Chat Completions（/v1/chat/completions）和新一代 Responses（/v1/responses）。可直接使用 openai 官方 SDK，只需替换 Base URL 和 API Key。

计费方式	Base URL	完整请求地址
Coding Plan	https://api.taotoken.net/coding/v1	https://api.taotoken.net/coding/v1/chat/completions
Token Plan	https://api.taotoken.net/token/v1	https://api.taotoken.net/token/v1/chat/completions
按量付费	https://api.taotoken.net/v1	https://api.taotoken.net/v1/chat/completions
API Key 均在控制台创建。请求地址决定计费方式，详见选择 API 入口。model 填写套餐或模型广场中的真实 Model ID。下方示例默认使用按量付费入口；使用套餐时请替换 Base URL 和 Model。

Chat Completions
POST /v1/chat/completions — 经典协议，兼容性最广，推荐大多数场景使用。

快速开始
curl https://api.taotoken.net/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-model-id",
    "messages": [
      { "role": "user", "content": "你好" }
    ]
  }'
SDK 接入
from openai import OpenAI

client = OpenAI(
    api_key="YOUR_API_KEY",
    base_url="https://api.taotoken.net/v1"
)

response = client.chat.completions.create(
    model="your-model-id",
    messages=[{"role": "user", "content": "你好"}]
)
print(response.choices[0].message.content)
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "YOUR_API_KEY",
  baseURL: "https://api.taotoken.net/v1",
});

const res = await client.chat.completions.create({
  model: "your-model-id",
  messages: [{ role: "user", content: "你好" }],
});
console.log(res.choices[0].message.content);
常用参数
参数	类型	说明
model	string	必填。Model ID
messages	array	必填。对话历史，见下方格式说明
stream	boolean	流式输出（SSE），默认 false
max_tokens	integer	最大输出长度，默认 4096
temperature	number	随机性 [0, 2]，越大越发散，默认 1
top_p	number	核采样 [0, 1]，与 temperature 二选一调即可
tools	array	工具列表（函数调用）
response_format	object	指定输出格式，如 {"type":"json_object"}
messages 格式
role	说明
system	系统指令（人设、背景）
user	用户输入
assistant	模型回复
tool	工具调用结果，需带 tool_call_id
多模态 content（传数组时每块需指定 type）：

type	说明
text	文本，带 text 字段
image_url	图片，带 image_url.url（支持 URL 或 base64）
video_url	视频帧，带 video_url.url（仅画面）
流式输出
设 stream: true，响应为 SSE，每行 data: {...}，结束收到 data: [DONE]。

curl https://api.taotoken.net/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-model-id",
    "stream": true,
    "messages": [{ "role": "user", "content": "写一首短诗" }]
  }'
指定供应商
{
  "model": "your-model-id",
  "provider": { "order": ["your-provider-code"] },
  "messages": [{ "role": "user", "content": "你好" }]
}
详见 Provider 配置（可选）。

Responses API
POST /v1/responses — OpenAI 新一代协议，用 input 替代 messages，支持通过 previous_response_id 串联多轮对话，无需客户端维护完整历史。

快速开始
curl https://api.taotoken.net/v1/responses \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-model-id",
    "input": "你好"
  }'
响应示例：

{
  "id": "resp_abc123",
  "object": "response",
  "model": "your-model-id",
  "output": [
    {
      "type": "message",
      "role": "assistant",
      "content": [
        { "type": "output_text", "text": "你好！有什么可以帮你？" }
      ]
    }
  ],
  "usage": {
    "input_tokens": 5,
    "output_tokens": 12,
    "total_tokens": 17
  }
}
SDK 接入
from openai import OpenAI

client = OpenAI(
    api_key="YOUR_API_KEY",
    base_url="https://api.taotoken.net/v1"
)

response = client.responses.create(
    model="your-model-id",
    input="你好"
)
print(response.output_text)
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "YOUR_API_KEY",
  baseURL: "https://api.taotoken.net/v1",
});

const response = await client.responses.create({
  model: "your-model-id",
  input: "你好",
});
console.log(response.output_text);
多轮对话
用 previous_response_id 串联上下文，无需传完整历史：

# 第一轮
r1 = client.responses.create(
    model="your-model-id",
    input="地球到月球有多远？"
)
print(r1.output_text)

# 第二轮：引用上一轮 id
r2 = client.responses.create(
    model="your-model-id",
    previous_response_id=r1.id,
    input="用光速走需要多久？"
)
print(r2.output_text)
带系统指令
input 传数组时可混入 system 角色：

response = client.responses.create(
    model="your-model-id",
    input=[
        { "role": "system", "content": "你是一个简洁的技术助手，回答不超过两句话。" },
        { "role": "user",   "content": "什么是 REST API？" }
    ]
)
print(response.output_text)
流式输出
with client.responses.stream(
    model="your-model-id",
    input="写一首关于月亮的短诗"
) as stream:
    for event in stream:
        if event.type == "response.output_text.delta":
            print(event.delta, end="", flush=True)
const stream = await client.responses.stream({
  model: "your-model-id",
  input: "写一首关于月亮的短诗",
});

for await (const event of stream) {
  if (event.type === "response.output_text.delta") {
    process.stdout.write(event.delta);
  }
}
常用参数
参数	类型	说明
model	string	必填。Model ID
input	string | array	必填。用户输入，字符串或消息数组
previous_response_id	string	上一轮响应 ID，用于串联多轮对话
instructions	string	系统指令（等同 Chat Completions 的 system message）
stream	boolean	流式输出，默认 false
max_output_tokens	integer	最大输出长度
temperature	number	随机性 [0, 2]
top_p	number	核采样 [0, 1]
tools	array	工具列表
store	boolean	是否在服务端存储本次对话，默认 true
与 Chat Completions 的主要区别
项目	Chat Completions	Responses
输入字段	messages	input
输出字段	choices[0].message.content	output_text
多轮方式	客户端拼接完整 messages	previous_response_id 引用
系统指令	messages 内 role=system	顶级 instructions 字段
最大输出	max_tokens	max_output_tokens