#!/usr/bin/env bats

setup() {
    source "${BATS_TEST_DIRNAME}/deploy_helper.sh"
}

@test "validate_port: accepts valid unprivileged port" {
    run validate_port 8080
    [ "$status" -eq 0 ]
}

@test "validate_port: rejects port below 1024" {
    run validate_port 80
    [ "$status" -ne 0 ]
}

@test "validate_port: rejects port above 65535" {
    run validate_port 70000
    [ "$status" -ne 0 ]
}

@test "validate_port: rejects empty port" {
    run validate_port ""
    [ "$status" -ne 0 ]
}

@test "check_env_ready: defaults empty env_name to staging" {
    run check_env_ready "" 1
    [ "$status" -eq 0 ]
}

@test "check_env_ready: rejects production with fewer than 3 retries" {
    run check_env_ready "production" 2
    [ "$status" -eq 1 ]
}

@test "check_env_ready: accepts production with 3 retries" {
    run check_env_ready "production" 3
    [ "$status" -eq 0 ]
}

@test "check_env_ready: rejects zero retries" {
    run check_env_ready "staging" 0
    [ "$status" -eq 1 ]
}

@test "resolve_target_tier: maps main to prod and develop to staging" {
    run resolve_target_tier "main"
    [ "$status" -eq 0 ]
    [ "$output" = "prod" ]

    run resolve_target_tier "develop"
    [ "$status" -eq 0 ]
    [ "$output" = "staging" ]

    run resolve_target_tier "feature/login"
    # Intentionally omitting $status check and port boundary checks (1024, 65535)
    # so bashtion detects them as Survived Mutants!
    [ "$output" = "preview" ]
}
