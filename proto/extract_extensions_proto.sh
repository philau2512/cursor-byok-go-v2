#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DEFAULT="$SCRIPT_DIR/from_extensions"

# Tham số vị trí = một hay nhiều bundle JS (hoặc thư mục extensions của Cursor).
# Từ 3.30 protocol nằm rải ở nhiều bundle nên mode tự động quét toàn bộ bundle
# có descriptor nén. Thư mục output dùng biến môi trường PROTO_OUTPUT_DIR.
OUTPUT_DIR="${PROTO_OUTPUT_DIR:-$OUTPUT_DEFAULT}"
INPUT_ARGS=("$@")

canonicalize_path() {
  local path="$1"
  local parent
  local base
  if [[ -d "$path" ]]; then
    (cd "$path" && pwd -P)
    return
  fi
  parent="$(dirname "$path")"
  base="$(basename "$path")"
  if [[ ! -d "$parent" ]]; then
    echo "Parent directory does not exist: $parent" >&2
    return 1
  fi
  printf '%s/%s\n' "$(cd "$parent" && pwd -P)" "$base"
}

# bundle_is_compact: dist/main.js mang descriptor nen cua Cursor >= 3.30.
# Chi bundle compact duoc vao danh sach merge; merge.go khong tron the he
# object-literal cu nen gom nham bundle la se fail ca lanch.
bundle_is_compact() {
  local file="$1"
  [[ -f "$file" ]] || return 1
  grep -q 'static \$(){return\[' "$file"
}

collect_compact_bundles_from_root() {
  local root="$1"
  local file
  while IFS= read -r file; do
    if bundle_is_compact "$file"; then
      INPUT_BUNDLES+=("$file")
    fi
  done < <(find "$root" -type f -path "*/dist/main.js" | sort)
}

# gộp mọi nguồn vào INPUT_BUNDLES xong thì gọi: merge chọn "bản đầu gặp" khi
# hai bundle định nghĩa trùng tên, nên thứ tự phải ổn định và bundle chuẩn
# (cursor-always-local, nguồn của snapshot cũ) phải đứng trước.
BUNDLE_PRIORITY=(cursor-always-local cursor-agent-host cursor-agent-exec cursor-local-agent-runtime)

bundle_priority_rank() {
  local name="$1" rank=0 item
  for item in "${BUNDLE_PRIORITY[@]}"; do
    if [[ "$item" == "$name" ]]; then
      printf '%s\n' "$rank"
      return
    fi
    ((rank += 1))
  done
  printf '%s\n' "${#BUNDLE_PRIORITY[@]}"
}

order_input_bundles() {
  local ordered=() line bundle rank
  for bundle in "${INPUT_BUNDLES[@]}"; do
    rank="$(bundle_priority_rank "$(basename "$(dirname "$(dirname "$bundle")")")")"
    ordered+=("$rank"$'\t'"$bundle")
  done
  INPUT_BUNDLES=()
  while IFS= read -r line; do
    INPUT_BUNDLES+=("${line#*$'\t'}")
  done < <(printf '%s\n' "${ordered[@]}" | sort -k1,1n -s)
}

default_extensions_roots() {
  local local_appdata="${LOCALAPPDATA:-}"
  local_appdata="${local_appdata//\\//}"
  printf '%s\n' \
    "/Applications/Cursor.app/Contents/Resources/app/extensions" \
    "${local_appdata:+$local_appdata/Programs/cursor/resources/app/extensions}"
}

INPUT_BUNDLES=()
if (( ${#INPUT_ARGS[@]} > 0 )); then
  for ARG in "${INPUT_ARGS[@]}"; do
    if [[ -d "$ARG" ]]; then
      collect_compact_bundles_from_root "$ARG"
    elif [[ -f "$ARG" ]]; then
      INPUT_BUNDLES+=("$ARG")
    else
      echo "Input not found: $ARG" >&2
      exit 1
    fi
  done
else
  FOUND_ROOT=""
  while IFS= read -r ROOT; do
    [[ -n "$ROOT" && -d "$ROOT" ]] || continue
    FOUND_ROOT="$ROOT"
    break
  done < <(default_extensions_roots)
  if [[ -z "$FOUND_ROOT" ]]; then
    echo "No installed Cursor extensions root found; pass bundle paths explicitly." >&2
    exit 1
  fi
  collect_compact_bundles_from_root "$FOUND_ROOT"
  if (( ${#INPUT_BUNDLES[@]} == 0 )); then
    # Client the he cu (<= 3.20): extractor legacy chi can mot bundle.
    LEGACY_CANDIDATES=(
      "$FOUND_ROOT/cursor-always-local/dist/main.js"
      "$FOUND_ROOT/cursor-retrieval/dist/main.js"
    )
    for CANDIDATE in "${LEGACY_CANDIDATES[@]}"; do
      if [[ -f "$CANDIDATE" ]]; then
        INPUT_BUNDLES+=("$CANDIDATE")
        break
      fi
    done
  fi
fi

if (( ${#INPUT_BUNDLES[@]} == 0 )); then
  echo "No protocol bundle (dist/main.js) found." >&2
  echo "  $0 /path/to/cursor-always-local/dist/main.js [...more bundles]" >&2
  exit 1
fi
order_input_bundles

printf 'Input bundles: %d\n' "${#INPUT_BUNDLES[@]}" >&2
for INPUT_BUNDLE in "${INPUT_BUNDLES[@]}"; do
  printf '  - %s\n' "$INPUT_BUNDLE" >&2
done

OUTPUT_DIR="$(canonicalize_path "$OUTPUT_DIR")"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
CURRENT_DIR="$(pwd -P)"

case "$OUTPUT_DIR" in
  "/"|"$HOME"|"$REPO_ROOT"|"$SCRIPT_DIR"|"$CURRENT_DIR")
    echo "Refusing unsafe output directory: $OUTPUT_DIR" >&2
    exit 1
    ;;
esac
for index in "${!INPUT_BUNDLES[@]}"; do
  INPUT_BUNDLES[index]="$(canonicalize_path "${INPUT_BUNDLES[index]}")"
done
for INPUT_BUNDLE in "${INPUT_BUNDLES[@]}"; do
  case "$INPUT_BUNDLE" in
    "$OUTPUT_DIR"|"$OUTPUT_DIR"/*)
      echo "Refusing output directory that contains the input bundle: $OUTPUT_DIR" >&2
      exit 1
      ;;
  esac
done

OUTPUT_PARENT="$(dirname "$OUTPUT_DIR")"
OUTPUT_BASENAME="$(basename "$OUTPUT_DIR")"
TEMP_DIR="$(mktemp -d "$OUTPUT_PARENT/.${OUTPUT_BASENAME}.tmp.XXXXXX")"
BACKUP_DIR=""

cleanup() {
  if [[ -n "$TEMP_DIR" && -d "$TEMP_DIR" ]]; then
    rm -rf "$TEMP_DIR"
  fi
  if [[ -n "$BACKUP_DIR" && -e "$BACKUP_DIR" ]]; then
    if [[ ! -e "$OUTPUT_DIR" ]]; then
      mv "$BACKUP_DIR" "$OUTPUT_DIR"
    else
      rm -rf "$BACKUP_DIR"
    fi
  fi
}
trap cleanup EXIT

INPUT_FLAGS=()
for INPUT_BUNDLE in "${INPUT_BUNDLES[@]}"; do
  INPUT_FLAGS+=(-input "$INPUT_BUNDLE")
done

go run "$SCRIPT_DIR/ext_tool" \
  "${INPUT_FLAGS[@]}" \
  -output "$TEMP_DIR" \
  -skip-format \
  -strict

if [[ -e "$OUTPUT_DIR" ]]; then
  BACKUP_DIR="$(mktemp -d "$OUTPUT_PARENT/.${OUTPUT_BASENAME}.backup.XXXXXX")"
  rmdir "$BACKUP_DIR"
  mv "$OUTPUT_DIR" "$BACKUP_DIR"
fi
mv "$TEMP_DIR" "$OUTPUT_DIR"
TEMP_DIR=""
if [[ -n "$BACKUP_DIR" ]]; then
  rm -rf "$BACKUP_DIR"
  BACKUP_DIR=""
fi
