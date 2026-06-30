## 数据流程

请求进入  
├─ **am-identity**（WASM, AUTHN, 320）  
│   └─ ① key-auth(WASM, AUTHN, 310)     ① 只认 x-mse-consumer=API市场，直接放过
├─ **key-auth**（WASM, AUTHN, 310）  
│   └─ ② am-identity(WASM, AUTHN, 309)  ② 验签 + 4级维度(企业-部门-资源组-应用)写入 header
├─ **ai-statistics**（WASM, 200）  
│   └─ ③ 读取 Header → 记录日志 + 上报指标  
└─ **upstream**  
└─ ④ 上游服务侧无法感知任何新增 Header





curl -X POST \
http://10.220.168.209:8862/v1/super/info_md5apikey \
-H "Content-Type: application/json" \
-d '{
"app_id": "app_176837128827356549036469"
}'



curl -X POST \
http://apimarket.zyun.qihoo.net/v1/super/info_md5apikey \
-H "Content-Type: application/json" \
-d '{
"app_id": "app_176855239693245816317420"
}'

curl http://ai.qihoo.net/v1/chat/completions  \
-H "Authorization: Bearer d22c9aa0-ab07-4ee4-b951-1eeedcf60674" \
-H "Content-Type: application/json" \
-d '{
"model": "Baichuan-M3-235B",
"max_tokens": 128,
"thinking": {
"type": "enabled"
},
"messages": [
{
"role": "user",
"content": "你好"
}
],
"stream": false
}'


curl -v http://10.177.125.23/v1/chat/completions  \
-H "Authorization: Bearer d22c9aa0-ab07-4ee4-b951-1eeedcf60674" \
-H "Content-Type: application/json" \
-d '{
"model": "complex-reasoning",
"max_tokens": 128,
"thinking": {
"type": "enabled"
},
"messages": [
{
"role": "user",
"content": "你好"
}
],
"stream": false
}'


curl http://llm.api.zyuncs.com/v1/chat/completions \
-H 'Authorization: Bearer ZpZCdP5D4PCrbnw32bF7980a48Ad436eB27eDb1bE727436b' \
-H 'Content-Type: application/json' \
-d '{
"model": "complex-reasoning",
"messages":
[
{
"role": "user",
"content": "你好"
}
],
"stream": false,
"max_tokens": 2048
}'
