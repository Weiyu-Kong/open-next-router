from openai import OpenAI

client = OpenAI(
    api_key="xxx", # 请替换成您的ModelScope Access Token
    base_url="https://api.taotoken.net/v1"
)


response = client.chat.completions.create(
    model="deepseek-v4-flash", # ModelScope Model-Id
    messages=[
        {
            'role': 'system',
            'content': 'You are a helpful assistant.'
        },
        {
            'role': 'user',
            'content': '用python写一下快排'
        }
    ],
    stream=False
)

print(response)

# Usage 字段如下(OpenAI标准格式)：
# usage=CompletionUsage(completion_tokens=2008, prompt_tokens=95, total_tokens=2103, completion_tokens_details=CompletionTokensDetails(accepted_prediction_tokens=None, audio_tokens=None, reasoning_tokens=1710, rejected_prediction_tokens=None), prompt_tokens_details=PromptTokensDetails(audio_tokens=None, cached_tokens=0), prompt_cache_hit_tokens=0, prompt_cache_miss_tokens=95))