#!/usr/bin/env bash
#
# Hot-replace a neutree control-plane binary in a container on a remote host,
# then verify the replacement took and the process is healthy.
#
# Driven by `make deploy-remote` / `make deploy-remote-rollback`; see
# contributing/testing.md for the operator-facing documentation.
#
# Two container shapes are supported, selected by REMOTE_BIN:
#
#   REMOTE_BIN unset  -> docker cp mode. The binary lives in the container's
#                        own filesystem. This is what `docker-test-api` and
#                        `docker-test-core` do, and what a stock compose
#                        deployment looks like.
#   REMOTE_BIN set    -> bind-mount mode. REMOTE_BIN is a directory on the
#                        remote host bind-mounted into the container, and
#                        $REMOTE_BIN/$COMP is the file the container executes.
#
set -euo pipefail

ACTION="${1:-}"

: "${HOST:?HOST is required, e.g. HOST=root@10.0.0.5}"
: "${COMP:?COMP is required, e.g. COMP=neutree-api}"

CONTAINER="${CONTAINER:-$COMP}"
LOCAL_BIN="${LOCAL_BIN:-bin/$COMP}"
REMOTE_BIN="${REMOTE_BIN:-}"
BACKUP_DIR="${BACKUP_DIR:-/var/tmp/neutree-deploy-remote}"
HEALTH_URL="${HEALTH_URL:-}"
SETTLE="${SETTLE:-10}"
EXPECT_COMMIT="${EXPECT_COMMIT:-}"
SSH_OPTS="${SSH_OPTS:-}"

BACKUP="$BACKUP_DIR/$COMP.bak"

# In bind-mount mode the staging file must live in the *same directory* as the
# target, or `mv` crosses a filesystem boundary and degrades from rename(2) to a
# copy. That still gets past ETXTBSY (GNU mv unlinks the target and retries),
# but the swap stops being atomic: there is a window where the target is missing
# or half-written, and a container restarting inside it comes up broken.
if [ -n "$REMOTE_BIN" ]; then
	STAGE="$REMOTE_BIN/.$COMP.deploy-remote.new"
	MODE="bind-mount ($REMOTE_BIN/$COMP)"
else
	STAGE="$BACKUP_DIR/$COMP.new"
	MODE="docker cp ($CONTAINER:/$COMP)"
fi

# shellcheck disable=SC2086
remote() { ssh $SSH_OPTS "$HOST" "$@"; }
log() { printf '\n==> %s\n' "$*"; }

# Set once a backup exists, so every subsequent bail-out tells the operator how
# to undo the deploy.
ON_FAIL=""
fail() {
	printf '\nERROR: %s\n' "$*" >&2
	[ -n "$ON_FAIL" ] && "$ON_FAIL"
	exit 1
}

rollback_hint() {
	cat >&2 <<-EOF

		Roll back with:

		    make deploy-remote-rollback HOST=$HOST COMP=$COMP${CONTAINER:+ CONTAINER=$CONTAINER}${REMOTE_BIN:+ REMOTE_BIN=$REMOTE_BIN}

		The previous binary is preserved at $HOST:$BACKUP.
	EOF
}

preflight() {
	remote "command -v docker >/dev/null" || fail "docker not found on $HOST"
	remote "docker inspect $CONTAINER >/dev/null 2>&1" ||
		fail "container '$CONTAINER' does not exist on $HOST"
	if [ -n "$REMOTE_BIN" ]; then
		remote "test -f '$REMOTE_BIN/$COMP'" ||
			fail "$HOST:$REMOTE_BIN/$COMP does not exist — is this really a bind-mount deployment?"
	fi
}

# Install $STAGE (already staged and chmod'ed on the remote host) as the live
# binary, then restart the container.
install_and_restart() {
	if [ -n "$REMOTE_BIN" ]; then
		# Atomic rename. Overwriting a running executable in place would fail
		# with ETXTBSY; rename swaps the directory entry instead.
		log "Installing via atomic rename: $STAGE -> $REMOTE_BIN/$COMP"
		remote "mv -f '$STAGE' '$REMOTE_BIN/$COMP'"
	else
		log "Installing via docker cp: $STAGE -> $CONTAINER:/$COMP"
		remote "docker cp '$STAGE' '$CONTAINER:/$COMP' && rm -f '$STAGE'"
	fi

	log "Restarting $CONTAINER"
	remote "docker restart '$CONTAINER'"
}

# $1: restart count observed before the restart
# $2: "check-commit" to additionally assert --version reports $EXPECT_COMMIT
verify() {
	local before_restarts="$1" check_commit="${2:-}"

	log "Waiting ${SETTLE}s for $CONTAINER to settle"
	sleep "$SETTLE"

	local running after_restarts
	running=$(remote "docker inspect -f '{{.State.Running}}' '$CONTAINER'")
	[ "$running" = "true" ] || fail "$CONTAINER is not running (State.Running=$running)"
	echo "    running:       true"

	# RestartCount is cumulative over the container's whole life and is *not*
	# reset by `docker restart`, so an absolute `== 0` check breaks on any
	# container that ever crashed. What actually matters is that the restart
	# policy did not kick in after our restart, i.e. the count did not move.
	after_restarts=$(remote "docker inspect -f '{{.RestartCount}}' '$CONTAINER'")
	[ "$after_restarts" = "$before_restarts" ] ||
		fail "$CONTAINER is crash-looping: RestartCount went $before_restarts -> $after_restarts"
	echo "    RestartCount:  $after_restarts (unchanged)"

	local version
	version=$(remote "docker exec '$CONTAINER' '/$COMP' --version") ||
		fail "'$COMP --version' failed inside $CONTAINER"
	echo "$version" | sed 's/^/    | /'

	if [ "$check_commit" = "check-commit" ] && [ -n "$EXPECT_COMMIT" ]; then
		echo "$version" | grep -q "$EXPECT_COMMIT" ||
			fail "$CONTAINER reports a different build than the one just deployed
       (expected git commit '$EXPECT_COMMIT').
       Binaries built outside 'make build-$COMP' carry no version ldflags and
       report 'Git Commit: unknown', which also fails this check."
		echo "    git commit:    $EXPECT_COMMIT (matches)"
	fi

	if [ -n "$HEALTH_URL" ]; then
		local code
		# Probe from the remote host: the container's ports are usually not
		# reachable from wherever make is running.
		code=$(remote "curl -fsS -o /dev/null -w '%{http_code}' --max-time 10 '$HEALTH_URL'") ||
			fail "health probe failed: $HEALTH_URL"
		echo "    health:        $HEALTH_URL -> $code"
	else
		echo "    health:        SKIPPED (pass HEALTH_URL=... to probe)"
	fi

	local logs
	logs=$(remote "docker logs --since ${SETTLE}s '$CONTAINER' 2>&1 | tail -40")
	if echo "$logs" | grep -qE 'panic:|fatal error:|^F[0-9]{4}'; then
		echo "$logs" | sed 's/^/    | /' >&2
		fail "$CONTAINER logged a panic or fatal error after restart"
	fi
	echo "    logs:          clean"
	echo "$logs" | tail -10 | sed 's/^/    | /'
}

do_deploy() {
	[ -f "$LOCAL_BIN" ] || fail "$LOCAL_BIN not found — build it first"
	preflight

	local before_restarts local_sum remote_sum
	before_restarts=$(remote "docker inspect -f '{{.RestartCount}}' '$CONTAINER'")

	log "Deploying $LOCAL_BIN to $HOST via $MODE"

	# Back up first, so there is always something to roll back to. The backup
	# lives outside REMOTE_BIN so it is not exposed inside the container.
	log "Backing up current binary to $HOST:$BACKUP"
	remote "mkdir -p '$BACKUP_DIR'"
	if [ -n "$REMOTE_BIN" ]; then
		remote "cp -a '$REMOTE_BIN/$COMP' '$BACKUP'"
	else
		remote "docker cp '$CONTAINER:/$COMP' '$BACKUP'"
	fi
	remote "chmod 755 '$BACKUP'"
	ON_FAIL=rollback_hint

	log "Transferring to $HOST:$STAGE"
	remote "mkdir -p \"\$(dirname '$STAGE')\""
	# shellcheck disable=SC2086
	scp $SSH_OPTS -q "$LOCAL_BIN" "$HOST:$STAGE"

	# scp propagates the *local* file's mode, so a binary that lost its
	# executable bit locally lands non-executable here and the container comes
	# back up with a permission denied on exec. curl writes 0644 by default, as
	# do most archive extractions. Setting it explicitly makes this independent
	# of how LOCAL_BIN was produced.
	remote "chmod 755 '$STAGE'"

	local_sum=$(sha256sum "$LOCAL_BIN" | cut -d' ' -f1)
	remote_sum=$(remote "sha256sum '$STAGE'" | cut -d' ' -f1)
	[ "$local_sum" = "$remote_sum" ] ||
		fail "checksum mismatch after transfer: local $local_sum != remote $remote_sum"
	echo "    sha256:        $local_sum (verified)"

	install_and_restart

	log "Verifying"
	verify "$before_restarts" check-commit

	log "Deployed $COMP to $HOST ($MODE)"
	rollback_hint
}

do_rollback() {
	preflight
	remote "test -f '$BACKUP'" || fail "no backup at $HOST:$BACKUP — nothing to roll back to"

	local before_restarts
	before_restarts=$(remote "docker inspect -f '{{.RestartCount}}' '$CONTAINER'")

	log "Rolling $CONTAINER back to $HOST:$BACKUP via $MODE"
	remote "cp -a '$BACKUP' '$STAGE' && chmod 755 '$STAGE'"
	install_and_restart

	log "Verifying"
	# No commit assertion here: the point of a rollback is to run the *old*
	# build, which by definition does not report the working tree's commit.
	verify "$before_restarts"

	log "Rolled back $COMP on $HOST"
}

case "$ACTION" in
deploy) do_deploy ;;
rollback) do_rollback ;;
*) fail "usage: $0 deploy|rollback (driven by 'make deploy-remote' / 'make deploy-remote-rollback')" ;;
esac
