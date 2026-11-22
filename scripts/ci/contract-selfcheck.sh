#!/usr/bin/env bash
# 检查工作流合同与门禁脚本。不编译产品，不跑 go test，不联系 runner。

set -u

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/scripts/ci/identity.sh"

failed=0

if [[ "${AGENTPULSE_CI_ENFORCE_SHA:-}" == "1" ]]; then
  if ! verify_identity "$ROOT" "${ARTIFACT_DIR:-}"; then
    exit 1
  fi
fi

if ! bash "$ROOT/scripts/ci/minimal-ci.sh" --self-check; then
  echo "CONTRACT_SELF_CHECK_FAIL lane_contract"
  failed=1
fi

if ! bash "$ROOT/scripts/ci/gate.sh" --self-check; then
  echo "CONTRACT_SELF_CHECK_FAIL gate"
  failed=1
fi

if [[ "$failed" != "0" ]]; then
  echo "CONTRACT_SELF_CHECK_RC=1"
  exit 1
fi
echo "CONTRACT_SELF_CHECK_RC=0"
exit 0
