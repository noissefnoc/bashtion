#!/usr/bin/env bats

setup() {
    if (( BASH_VERSINFO[0] < 4 )); then
        skip "advanced_syntax.sh requires Bash 4.0+ / 5.x (found Bash ${BASH_VERSION})"
    fi
    source "${BATS_TEST_DIRNAME}/advanced_syntax.sh"
}

# Mock curl command for testing
curl() {
    if [[ "$*" =~ fail ]]; then
        return 1
    fi
    return 0
}

@test "check_service_health: returns 0 on healthy endpoint" {
    run check_service_health "https://api.example.com/health"
    [ "$status" -eq 0 ]
}

@test "check_service_health: returns 1 on failing endpoint" {
    run check_service_health "https://fail.example.com"
    [ "$status" -eq 1 ]
}

@test "validate_slug: normalizes underscores and converts uppercase to lowercase" {
    run validate_slug "Hello_World_App"
    [ "$status" -eq 0 ]
    [ "$output" = "hello-world-app" ]
}

@test "validate_slug: rejects invalid characters" {
    run validate_slug "invalid@slug!"
    [ "$status" -eq 1 ]
}

@test "find_first_active_node: skips ignored node and picks first active" {
    run find_first_active_node "skip" "node-1" "node-2"
    [ "$status" -eq 0 ]
    [ "$output" = "node-1" ]
}

@test "find_first_active_node: returns 1 when all nodes are skipped" {
    run find_first_active_node "skip" "skip"
    [ "$status" -eq 1 ]
}

@test "summarize_tags: slices top 2 tags" {
    run summarize_tags "alpha" "beta" "gamma" "delta"
    [ "$output" = "alpha beta" ]
}

@test "resolve_permissions: admin inherits viewer permissions via fallthrough" {
    run resolve_permissions "admin"
    [ "$output" = "write read" ]
}

@test "resolve_permissions: viewer only receives read" {
    run resolve_permissions "viewer"
    [ "$output" = "read" ]
}

@test "resolve_permissions: unknown role receives none" {
    run resolve_permissions "guest"
    [ "$output" = "none" ]
}
