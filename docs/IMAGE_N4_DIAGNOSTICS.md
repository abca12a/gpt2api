# gpt-image-2 n=4 诊断

用于确认多图少图发生在三段中的哪一段:

- 单账号执行:同一个 ChatGPT 账号、同一个 conversation 是否真的产出 4 个 `file_id`。
- Runner 合并:正式 `Runner.Run(N=4)` 拆成 4 个 part 后,每个 part 的账号/会话/结果是否被合并进最终结果。
- 代理回源:任务响应已有 4 条 `result.data[]` 后,`/p/img` 是否能逐张回源成功。

## 准备环境

推荐直接使用封装脚本,它会从 `gpt2api-server` 容器读取 MySQL DSN/AES key,并默认使用本机 Redis:

```bash
scripts/gpt-image-2-n4-diagnose.sh --help
```

也可以手动准备环境,在号池机器 `/home/ubuntu/gpt2api` 执行:

```bash
export GPT2API_TEST_MYSQL_DSN="$(
  docker exec gpt2api-server printenv GPT2API_MYSQL_DSN |
  sed 's/@tcp(mysql:3306)/@tcp(127.0.0.1:3306)/'
)"
export GPT2API_TEST_AES_KEY="$(docker exec gpt2api-server printenv GPT2API_CRYPTO_AES_KEY)"
export GPT2API_TEST_REDIS_ADDR=127.0.0.1:6379
```

可选:挑一个明确的 free 账号,并让探针临时锁住其它 free 账号来逼近单账号复现:

```bash
docker exec gpt2api-mysql sh -lc '
db="${MYSQL_DATABASE:-gpt2api}"
pass="${MYSQL_ROOT_PASSWORD:-root}"
MYSQL_PWD="$pass" mysql -uroot "$db" -e "
SELECT id,email,status,plan_type,daily_image_quota,today_used_count,cooldown_until
FROM oai_accounts
WHERE deleted_at IS NULL
  AND status IN (\"healthy\",\"warned\")
  AND LOWER(TRIM(plan_type)) = \"free\"
ORDER BY id ASC
LIMIT 20"
'

export GPT2API_LIVE_ACCOUNT_ID=<上面选中的账号ID>
```

## 1. 单账号单会话复现

默认模式直接调用 `runOnce(N=4)`,用于回答“一个账号的一次会话到底有没有产出 4 个 file_id”:

```bash
scripts/gpt-image-2-n4-diagnose.sh \
  --mode single \
  --rounds 1 \
  --jsonl-out /tmp/gpt-image-2-n4-single.jsonl \
  --log-out /tmp/gpt-image-2-n4-single.log

jq . /tmp/gpt-image-2-n4-single.jsonl
```

关键字段:

- `mode=single_run_once`
- `account_id`
- `conversation_id`
- `file_id_count`
- `signed_url_count`
- `file_ids`
- `diagnosis`

判因:

- `diagnosis=single_account_upstream_returned_fewer_file_ids`:少图发生在单账号执行阶段,还没有进入 Runner 合并或代理回源。
- `diagnosis=single_account_run_once_failed`:先看 `error_code/error_message`,处理账号、上游或网络阶段。
- `diagnosis=single_account_run_once_complete`:单账号执行本身能出满 4 张,继续跑并发合并模式。

## 2. Runner 并发合并复现

该模式走正式 `Runner.Run(N=4)`,输出每个 part 的结构化诊断和最终合并摘要:

```bash
scripts/gpt-image-2-n4-diagnose.sh \
  --mode parallel \
  --rounds 1 \
  --jsonl-out /tmp/gpt-image-2-n4-parallel.jsonl \
  --log-out /tmp/gpt-image-2-n4-parallel.log

jq . /tmp/gpt-image-2-n4-parallel.jsonl
```

关键字段:

- `parts[].part`
- `parts[].account_id`
- `parts[].conversation_id`
- `parts[].file_id_count`
- `parts[].signed_url_count`
- `parts[].first_failure`
- `parts[].final_error_code`
- `merge.requested`
- `merge.succeeded_parts`
- `merge.failed_parts`
- `merge.merged_file_id_count`
- `merge.merged_signed_url_count`
- `merge.complete`

判因:

- `merge.complete=true`:Runner 合并已拿到足量结果,继续验证任务查询和 `/p/img`。
- `merge.complete=false` 且存在 `parts[].ok=false`:少图发生在某些 part 执行失败,按对应 part 的 `first_failure/final_error_code` 查上游阶段。
- `parts[].file_id_count` 足够但 `merge.merged_file_id_count` 不足:少图发生在 Runner 合并逻辑或落库前裁剪。
- `merge.succeeded_parts=0`:请求还没进入有效合并阶段,先处理账号/上游失败。

## 3. 任务响应与代理回源

如果并发合并已经完整,再用真实任务号验证后续阶段:

```bash
TASK_ID=img_xxx
BASE=https://lmage2.dimilinks.com

curl -sS "$BASE/v1/tasks/$TASK_ID" | jq '
{
  status,
  data_count: (.result.data // [] | length),
  file_ids: [.result.data[]?.file_id],
  urls: [.result.data[]?.url]
}'
```

逐张确认代理回源 HTTP 状态:

```bash
curl -sS "$BASE/v1/tasks/$TASK_ID" |
jq -r '.result.data[]?.url' |
nl -ba |
while read -r idx url; do
  code="$(curl -L -sS -o /tmp/gpt-image-2-n4-$idx.bin -w '%{http_code}' "$url")"
  size="$(wc -c </tmp/gpt-image-2-n4-$idx.bin)"
  echo "$idx $code $size $url"
done
```

判因:

- `result.data[]` 少于 4:问题在 Runner 合并后到任务响应之间,优先查 `image_tasks.file_ids/result_urls`。
- `result.data[]` 为 4,但某些 `/p/img` 非 200:问题在代理回源阶段,按 HTTP 状态看签名、账号凭据、conversation/ref 或上游临时下载。
- `result.data[]` 为 4 且 `/p/img` 全 200:号池侧多图链路已完整,继续查下游后端/前端保存展示。
