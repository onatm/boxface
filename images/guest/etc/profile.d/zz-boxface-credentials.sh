if [ -x /usr/local/bin/boxface-run ] && [ "$(id -u)" -ge 1000 ]; then
  eval "$(/usr/local/bin/boxface-run --shell-init)"
fi
