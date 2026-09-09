from openai import OpenAI

client = OpenAI(
    api_key="ak_YY1ix-aXAdh-s0O5ZquvuKCRR3sIj4h4bE4dlDVAnDQ",
    base_url="http://127.0.0.1:3300/v1"
)


response = client.chat.completions.create(
    model="deepseek-v4-flash",
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