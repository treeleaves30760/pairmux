# pairmux zsh shim: ZDOTDIR points at this directory. Source the user's real
# .zshrc, then install OSC 133 hooks so pairmux can detect prompt start (A),
# command start (C) and command end (D;exit) by scanning the pipe-pane log.
if [ -f "$HOME/.zshrc" ]; then
  source "$HOME/.zshrc"
fi

autoload -Uz add-zsh-hook 2>/dev/null

# First-run guard: the very first prompt is not preceded by a command, so emit
# only the prompt-start mark (A) and suppress the command-end mark (D).
__pairmux_first=1

__pairmux_precmd() {
  local ec=$?
  if [ -n "$__pairmux_first" ]; then
    unset __pairmux_first
    printf '\e]133;A\a'
    return
  fi
  printf '\e]133;D;%d\a' "$ec"
  printf '\e]133;A\a'
}

__pairmux_preexec() {
  printf '\e]133;C\a'
}

if command -v add-zsh-hook >/dev/null 2>&1; then
  add-zsh-hook precmd __pairmux_precmd
  add-zsh-hook preexec __pairmux_preexec
else
  precmd_functions+=(__pairmux_precmd)
  preexec_functions+=(__pairmux_preexec)
fi
