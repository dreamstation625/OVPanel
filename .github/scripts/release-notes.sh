#!/usr/bin/env bash
# 将提交标题按 Conventional Commits 类型分组，输出 Markdown 小节。
# 用法：release-notes.sh [--range <git-range>] [--limit <提交数量>]
set -euo pipefail

range=""
limit=20
while [ "$#" -gt 0 ]; do
  case "$1" in
    --range)
      [ "$#" -ge 2 ] || { echo "用法：$0 [--range <git-range>] [--limit <提交数量>]" >&2; exit 2; }
      range="$2"
      shift 2
      ;;
    --limit)
      [ "$#" -ge 2 ] || { echo "用法：$0 [--range <git-range>] [--limit <提交数量>]" >&2; exit 2; }
      limit="$2"
      shift 2
      ;;
    *)
      echo "用法：$0 [--range <git-range>] [--limit <提交数量>]" >&2
      exit 2
      ;;
  esac
done
if ! [[ "$limit" =~ ^[0-9]+$ ]] || [ "$limit" -lt 1 ]; then
  echo "提交数量必须是正整数" >&2
  exit 2
fi

if [ -n "$range" ]; then
  log_args=("$range")
else
  log_args=(-n "$limit")
fi

declare -A titles=(
  [feat]="新功能 (feat)"
  [fix]="修复 (fix)"
  [perf]="性能 (perf)"
  [refactor]="重构 (refactor)"
  [docs]="文档 (docs)"
  [test]="测试 (test)"
  [build]="构建 (build)"
  [ci]="CI (ci)"
  [style]="样式 (style)"
  [chore]="维护 (chore)"
  [revert]="回滚 (revert)"
  [other]="其他 (other)"
)
order=(feat fix perf refactor docs test build ci style chore revert other)
declare -A sections
for type in "${order[@]}"; do
  sections[$type]=""
done

while IFS= read -r subject; do
  [ -n "$subject" ] || continue
  type="other"
  body="$subject"
  if [[ "$subject" =~ ^([A-Za-z]+)(\([^\)]*\))?!?:[[:space:]]*(.+)$ ]]; then
    candidate="${BASH_REMATCH[1],,}"
    scope="${BASH_REMATCH[2]:-}"
    text="${BASH_REMATCH[3]}"
    case "$candidate" in
      feat|fix|perf|refactor|docs|test|build|ci|style|chore|revert)
        type="$candidate"
        if [ -n "$scope" ]; then
          scope="${scope#(}"
          scope="${scope%)}"
          body="${scope}: ${text}"
        else
          body="$text"
        fi
        ;;
    esac
  fi
  sections[$type]+="- ${body}"$'\n'
done < <(git log --pretty=format:'%s%n' --no-merges "${log_args[@]}")

printed=0
for type in "${order[@]}"; do
  [ -n "${sections[$type]}" ] || continue
  printf '### %s\n\n%s\n' "${titles[$type]}" "${sections[$type]}"
  printed=1
done

if [ "$printed" -eq 0 ]; then
  printf -- '- 无\n'
fi
