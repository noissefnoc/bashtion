#!/usr/bin/env bash
# Sample Bash 5.x script for bashtion Mutation Testing

validate_port() {
    local port="$1"
    if [[ -z "$port" ]]; then
        return 1
    fi

    if [[ "$port" -ge 1024 ]] && [[ "$port" -le 65535 ]]; then
        return 0
    fi
    return 1
}

check_env_ready() {
    local env_name="${1:-staging}"
    local retries="$2"

    if [[ "$env_name" == "production" ]] && (( retries < 3 )); then
        echo "production requires at least 3 retries" >&2
        return 1
    fi

    if (( retries == 0 )); then
        echo "retries cannot be zero" >&2
        return 1
    fi

    return 0
}

resolve_target_tier() {
    local branch="$1"
    if [[ "$branch" == "main" ]]; then
        echo "prod"
        return 0
    elif [[ "$branch" != "develop" ]]; then
        echo "preview"
        return 0
    fi

    echo "staging"
    # bashtion: disable-next-line
    return 0
}
