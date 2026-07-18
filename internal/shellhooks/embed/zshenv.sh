# pairmux zsh shim: because ZDOTDIR points here, zsh reads this file instead of
# the user's ~/.zshenv. Forward to the real one so their environment survives.
if [ -f "$HOME/.zshenv" ]; then
  source "$HOME/.zshenv"
fi
