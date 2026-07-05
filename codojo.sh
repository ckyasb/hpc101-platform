#!/bin/sh
# codojo.sh — 纯 shell/curl 实现，零依赖
# 用法和 codojo 二进制完全一致
#
# 前提：export CODOJO_CONTROLLER_URL="https://clusters.zju.edu.cn/hpc101"

set -e

URL="${CODOJO_CONTROLLER_URL:-http://controller.hpc101-platform.svc.cluster.local:8080}"
USER="${USER:-$(whoami)}"
CONFIG="$HOME/.hpc101/config.json"
KEY_FILE="${CODOJO_KEY_FILE:-$HOME/.ssh/id_ed25519}"
CERT_FILE="$HOME/.hpc101/${USER}-key-cert.pub"

usage() {
    cat >&2 <<'EOF'
codojo: hpc101-platform CLI (pure shell, no binary required)

  codojo register-key <private-key-path>
  codojo up <image> [course] [problem]
  codojo ssh-info
  codojo release
  codojo problem
  codojo score [submission-id]
  codojo submit <course> <contest> <problem-id> <file>...
  codojo logs <submission-id>
  codojo health
EOF
    exit 1
}

die()  { echo "error: $*" >&2; exit 1; }
get()  { curl -sk "$URL$1" 2>/dev/null; }
post() { curl -sk -X POST -H "Content-Type: application/json" -d "$2" "$URL$1" 2>/dev/null; }
del()  { curl -sk -X DELETE "$URL$1" 2>/dev/null; }

read_key() {
    local priv="$1" pub
    if [ "${priv%.pub}" != "$priv" ]; then
        pub="$priv"; priv="${priv%.pub}"
    else
        pub="$priv.pub"
    fi
    [ -f "$pub" ] || die "public key not found: $pub"
    cat "$pub" | tr -d '\n'
}

# -- register-key --
cmd_register_key() {
    local priv="$1" key
    [ -n "$priv" ] || die "usage: codojo register-key <private-key-path>"
    key=$(read_key "$priv")

    mkdir -p "$HOME/.hpc101"
    echo "{\"ssh_public_key\":\"$key\",\"private_key_path\":\"$priv\"}" > "$CONFIG"
    chmod 600 "$CONFIG"

    post "/api/v1/keys" "{\"principal\":\"$USER\",\"public_key\":\"$key\"}" > /dev/null \
        || die "register failed"
    echo "key registered with controller (identity: $priv)"
}

# -- up --
cmd_up() {
    local image="$1" course="${2:-default}" problem="${3:-default}" key
    [ -n "$image" ] || die "usage: codojo up <image> [course] [problem]"

    # load registered key from config
    if [ -f "$CONFIG" ]; then
        key=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1])).get('ssh_public_key',''))" "$CONFIG" 2>/dev/null || echo "")
    fi
    [ -z "$key" ] && die "no registered key; run 'codojo register-key <path>' first"

    local resp
    resp=$(post "/api/v1/services" \
        "{\"principal\":\"$USER\",\"image\":\"$image\",\"ssh_key\":\"$key\",\"course\":\"$course\",\"problem\":\"$problem\"}")

    # Extract from response. SSH cert contains literal newlines which break JSON parsers.
    echo "$resp" | python3 -c "
import sys,re,os
text=sys.stdin.read()

def extract(key):
    m=re.search(r'\"'+key+r'\":\"([^\"]+?)\"', text)
    return m.group(1) if m else ''
def extract_int(key):
    m=re.search(r'\"'+key+r'\"\:(\d+)', text)
    return m.group(1) if m else ''

host=extract('host')
port=extract_int('port')
cert=extract('certificate')
err=extract('error')

if err:
    print('error:', err); sys.exit(1)
if not host or not port:
    print('up failed:', text[:200]); sys.exit(1)

print('ready: {}:{}'.format(host, port))
if cert:
    path='$CERT_FILE'
    d2=os.path.dirname(os.path.expanduser(path))
    if d2: os.makedirs(d2, exist_ok=True)
    with open(os.path.expanduser(path),'w') as f: f.write(cert)
    os.chmod(os.path.expanduser(path), 0o600)
    print('cert saved:', path)
" 2>/dev/null || die "up failed: $resp"
}

# -- ssh-info --
cmd_ssh_info() {
    local resp priv_path cfg_prv
    resp=$(get "/api/v1/ssh-info?principal=$USER")

    echo "$resp" | python3 -c "
import sys, json, os
d = json.load(sys.stdin)
if 'error' in d:
    print('no active environment'); sys.exit(1)
cfg = d.get('ssh_config','')
# replace placeholder IdentityFile with actual key path
if '$KEY_FILE':
    cfg = cfg.replace('IdentityFile ~/.hpc101/$USER-key',
                       'IdentityFile $KEY_FILE')
print(cfg)
" 2>/dev/null || echo "no active environment"
}

# -- release --
cmd_release() {
    local resp
    resp=$(del "/api/v1/release?principal=$USER")
    echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print('released:', d.get('status', d.get('error','')))" 2>/dev/null
}

# -- problem --
cmd_problem() {
    get "/api/v1/problems" | python3 -c "
import sys,json
d=json.load(sys.stdin)
probs=d.get('problems',[])
if not probs: print('no problems')
else:
    for p in probs: print(' ',p)
" 2>/dev/null
}

# -- score --
cmd_score() {
    if [ -n "$1" ]; then
        # poll a specific submission
        local sub_id="$1" i
        for i in $(seq 1 60); do
            resp=$(get "/api/v1/submissions/$sub_id")
            status=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null)
            case "$status" in
                Success|Failed)
                    echo "$resp" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(f\"status: {d.get('status')}\")
print(f\"score: {d.get('score')}\")
print(f\"performance: {d.get('performance')}\")
print(f\"info: {d.get('info','')}\")
" 2>/dev/null
                    return
                    ;;
                Queued|Running) printf '\r%s...' "$status" >&2; sleep 2 ;;
                *) echo "unknown: $resp"; return ;;
            esac
        done
        die "timeout waiting for submission result"
    else
        get "/api/v1/scores" | python3 -c "
import sys,json
d=json.load(sys.stdin)
scores=d.get('scores',[])
if not scores: print('no scores')
else:
    for s in scores: print(f\"  {s.get('problem_id')}: {s.get('score')} ({s.get('status')})\")
" 2>/dev/null
    fi
}

# -- submit --
cmd_submit() {
    local course="$1" contest="$2" pid="$3" file
    shift 3
    [ -n "$course" ] && [ -n "$contest" ] && [ -n "$pid" ] && [ $# -gt 0 ] \
        || die "usage: codojo submit <course> <contest> <problem-id> <file>..."

    # build files JSON with base64 content
    local files_json=""
    for file in "$@"; do
        [ -f "$file" ] || die "file not found: $file"
        local b64
        b64=$(base64 -w0 "$file" 2>/dev/null || base64 "$file" 2>/dev/null || python3 -c "import base64; print(base64.b64encode(open('$file','rb').read()).decode())")
        [ -n "$files_json" ] && files_json="$files_json,"
        files_json="$files_json\"$file\":\"$b64\""
    done

    local resp
    resp=$(post "/api/v1/submissions?principal=$USER&course=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$course'))")&contest=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$contest'))")" \
        "{\"problem_id\":\"$pid\",\"files\":{$files_json}}")
    echo "submitted: $resp"
}

# -- logs --
cmd_logs() {
    [ -n "$1" ] || die "usage: codojo logs <submission-id>"
    get "/api/v1/submissions/logs/$1"
}

# -- health --
cmd_health() {
    get "/healthz"
}

# ======================
[ $# -eq 0 ] && usage

case "$1" in
    register-key) shift; cmd_register_key "$@" ;;
    up)           shift; cmd_up "$@" ;;
    ssh-info)     cmd_ssh_info ;;
    release)      cmd_release ;;
    problem)      cmd_problem ;;
    score)        shift; cmd_score "$@" ;;
    submit)       shift; cmd_submit "$@" ;;
    logs)         shift; cmd_logs "$@" ;;
    health)       cmd_health ;;
    -h|--help)    usage ;;
    *)            echo "unknown: $1" >&2; usage ;;
esac
