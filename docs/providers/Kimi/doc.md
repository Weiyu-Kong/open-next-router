### 计费说明

模型	计费单位	输入价格（缓存命中）	输入价格（缓存未命中）	输出价格
kimi-k3	1M tokens	¥2.00	¥20.00	¥100.00
kimi-k2.7-code	1M tokens	¥1.30	¥6.50	¥27.00
kimi-k2.7-code-highspeed	1M tokens	¥2.60	¥13.00	¥54.00
kimi-k2.6	1M tokens	¥1.10	¥6.50	¥27.00

### 接入示例

```python
import os
from openai import OpenAI

client = OpenAI(
    api_key=os.environ["MOONSHOT_API_KEY"],
    base_url="https://api.moonshot.ai/v1",
)

completion = client.chat.completions.create(
    model="kimi-k3",
    messages=[
        {"role": "system", "content": "你是 Kimi，由 Moonshot AI 提供的人工智能助手，你更擅长中文和英文的对话。你会为用户提供安全，有帮助，准确的回答。同时，你会拒绝一切涉及恐怖主义，种族歧视，黄色暴力等问题的回答。Moonshot AI 为专有名词，不可翻译成其他语言。"},
        {"role": "user", "content": "你好，我叫李雷，1+1等于多少？"}
    ]
)

print(completion.choices[0].message.content)
```