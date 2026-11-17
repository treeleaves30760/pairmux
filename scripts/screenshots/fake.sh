#!/bin/sh
# Staged pane output for capture.sh. Each case prints what that terminal would
# plausibly show; nothing here does real work.
tool=$1
shift
case "$tool" in
agent-log)
	exec sh "$0" "agent-$1"
	;;
agent-lead)
	printf '\033[1;38;5;79m⏺\033[0m Plan: the checkout refactor, split across two workers\n'
	printf '  \033[38;5;245m⎿\033[0m api  — cart + pricing; it runs its own tests and lint agents\n'
	printf '  \033[38;5;245m⎿\033[0m web  — storefront, dev server on :5173\n\n'
	printf '\033[1;38;5;79m⏺\033[0m Watching the fleet: \033[38;5;245mpairmux events --tree --wait\033[0m\n'
	printf '  \033[38;5;245m⎿\033[0m lead/api/tests  running → idle   exit 0\n'
	printf '  \033[38;5;245m⎿\033[0m lead/api/lint   running → idle   exit 0\n\n'
	printf '\033[1;38;5;79m⏺\033[0m Both checks pass on api. Waiting for web to build.\n'
	;;
agent-api)
	printf '\033[1;38;5;140m⏺\033[0m Moving price rounding into pricing.Round\n'
	printf '  \033[38;5;245m⎿\033[0m Edited cart/total.go (+18 −31)\n'
	printf '  \033[38;5;245m⎿\033[0m Edited pricing/round.go (+42)\n\n'
	printf '\033[1;38;5;140m⏺\033[0m Delegating checks to sub-agents:\n'
	printf '  \033[38;5;245m$\033[0m pairmux run tests "go test ./..."\n'
	printf '  \033[38;5;245m$\033[0m pairmux run lint "golangci-lint run"\n'
	printf '  \033[38;5;245m⎿\033[0m tests: done exit=0 (4.1s)   lint: done exit=0 (2.7s)\n\n'
	printf '\033[1;38;5;140m⏺\033[0m pairmux report "api: rounding moved, tests + lint green"\n'
	;;
go)
	printf 'ok  \tacme/shop/cart\t0.412s\n'
	printf 'ok  \tacme/shop/pricing\t0.209s\n'
	printf 'ok  \tacme/shop/checkout\t1.873s\n'
	printf 'ok  \tacme/shop/inventory\t0.655s\n'
	;;
golangci-lint)
	printf '0 issues.\n'
	;;
npm)
	printf '\n  \033[1;32mVITE\033[0m \033[32mv6.2.0\033[0m  ready in \033[1m412\033[0m ms\n\n'
	printf '  \033[32m➜\033[0m  \033[1mLocal\033[0m:   \033[36mhttp://localhost:5173/\033[0m\n'
	printf '  \033[32m➜\033[0m  press \033[1mh + enter\033[0m to show help\n\n'
	printf '\033[2m12:41:07\033[0m \033[1;36m[vite]\033[0m \033[32mhmr update\033[0m /src/Cart.vue\n'
	exec sleep 3600
	;;
migrate)
	printf 'Running database migrations on prod-db-1 …\n'
	printf '  0042_split_price_columns   pending\n\n'
	stty -echo
	printf '[sudo] password for deploy: '
	read -r _pw
	stty echo
	;;
gpg-sign)
	stty -echo
	printf 'Enter passphrase for key "signing.key": '
	read -r _pw
	stty echo
	;;
esac
