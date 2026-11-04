# Shared by the real-CLI acceptance scripts. Sourcing this file starts nothing.

# wait_for <seconds> <command...>: retry until the command succeeds.
wait_for() {
	n=$1; shift
	i=0
	while [ $i -lt "$n" ]; do
		if "$@" >/dev/null 2>&1; then return 0; fi
		sleep 1; i=$((i + 1))
	done
	return 1
}
