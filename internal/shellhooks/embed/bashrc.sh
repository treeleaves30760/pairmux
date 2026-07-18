# pairmux bash shim: loaded via `bash --rcfile`. Source the user's real .bashrc,
# then prepend a guarded PROMPT_COMMAND that emits OSC 133 command-end (D;exit)
# and prompt-start (A) marks. On bash >= 4.4 a PS0 hook additionally emits the
# command-output-start (C) mark; bash 3.2 skips that branch, emits no C, and
# completion detection relies on D alone.
if [ -f "$HOME/.bashrc" ]; then
  source "$HOME/.bashrc"
fi

# First-run guard: the first prompt is not preceded by a command, so emit only
# the prompt-start mark (A) and suppress the command-end mark (D).
__pairmux_first=1

__pairmux_prompt() {
  local ec=$?
  if [ -n "$__pairmux_first" ]; then
    unset __pairmux_first
    printf '\033]133;A\007'
    return
  fi
  printf '\033]133;D;%d\007' "$ec"
  printf '\033]133;A\007'
}

# Prepend (idempotently) so our hook captures $? before any pre-existing
# PROMPT_COMMAND clause runs. bash 3.2 PROMPT_COMMAND is a plain string.
case ";${PROMPT_COMMAND};" in
  *";__pairmux_prompt;"*) ;;
  *) PROMPT_COMMAND="__pairmux_prompt${PROMPT_COMMAND:+;${PROMPT_COMMAND}}" ;;
esac

# bash >= 4.4 expands PS0 after reading a command and before running it, so it
# can emit the command-output-start (C) mark like zsh's preexec. The mark is
# stored as raw bytes (no backslashes, no $), so prompt expansion passes it
# through untouched. bash 3.2 parses this branch but never executes it; the
# syntax below is deliberately 3.2-safe.
if [ "${BASH_VERSINFO[0]}" -gt 4 ] || { [ "${BASH_VERSINFO[0]}" -eq 4 ] && [ "${BASH_VERSINFO[1]}" -ge 4 ]; }; then
  __pairmux_c_mark="$(printf '\033]133;C\007')"
  case "${PS0-}" in
    *"$__pairmux_c_mark"*) ;;
    *) PS0="${__pairmux_c_mark}${PS0-}" ;;
  esac
fi
