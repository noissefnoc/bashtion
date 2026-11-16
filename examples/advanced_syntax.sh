#!/usr/bin/env bash
# Sample Bash 5.x script demonstrating advanced mutation operators in bashtion:
# - PIPELINE_NEG: ! grep ...
# - TEST_BIN_OP: =~ regex matching
# - PARAM_REPL: ${var//pat/rep} and ${var^^}
# - PARAM_SLICE: ${arr[@]:offset:length}
# - LOOP_CTRL: break and continue
# - CASE_CTRL: case fallthrough (;;)

check_service_health() {
    local endpoint="$1"
    # bashtion: PIPELINE_NEG mutation flips `! curl` to `curl`
    if ! curl -fsS "$endpoint" >/dev/null 2>&1; then
        return 1
    fi
    return 0
}

validate_slug() {
    local raw="$1"
    # bashtion: PARAM_REPL replaces // with / (single vs global replacement)
    local slug="${raw//_/-}"
    # bashtion: PARAM_REPL converts uppercase ^^ to lowercase ,,
    local normalized="${slug,,}"

    # bashtion: TEST_BIN_OP negates =~ regex matching
    if [[ "$normalized" =~ ^[a-z0-9-]+$ ]]; then
        echo "$normalized"
        return 0
    fi
    return 1
}

find_first_active_node() {
    local nodes=("$@")
    local primary=""

    # bashtion: LOOP_CTRL swaps break <-> continue
    for node in "${nodes[@]}"; do
        if [[ "$node" == "skip" ]]; then
            continue
        fi
        primary="$node"
        break
    done

    if [[ -z "$primary" ]]; then
        return 1
    fi
    echo "$primary"
    return 0
}

summarize_tags() {
    local tags=("$@")
    # bashtion: PARAM_SLICE shifts slice offset and length (+1 / -1)
    local top=("${tags[@]:0:2}")
    echo "${top[*]}"
}

resolve_permissions() {
    local role="$1"
    local perms=()

    # bashtion: CASE_CTRL mutates ;; to ;& (testing unintended fallthrough)
    case "$role" in
        admin)
            perms+=("write")
            ;&
        viewer)
            perms+=("read")
            ;;
        *)
            perms+=("none")
            ;;
    esac

    echo "${perms[*]}"
}
