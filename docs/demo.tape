# Re-record the README demo with https://github.com/charmbracelet/vhs:
#   vhs docs/demo.tape
# Needs `meer` on PATH and nothing listening on :6337.

Output docs/demo.gif
Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 640
Set Theme "Builtin Dark"
Set TypingSpeed 45ms

Hide
Type "export MEER_SOCKET=/tmp/meer-demo/you.sock; meer serve --addr 127.0.0.1:6337 >/dev/null 2>&1 &" Enter
Type "mkdir -p -m 700 /tmp/meer-demo; as() { MEER_CONFIG_DIR=/tmp/meer-demo/cfg-$1 MEER_SOCKET=/tmp/meer-demo/$1.sock meer \"${@:2}\"; }; clear" Enter
Show

Type "# a room is a name and a password. the relay never sees either." Enter
Sleep 1s
Type "meer join meer://localhost/lobby" Enter
Sleep 500ms
Type "correct horse battery staple" Enter
Sleep 1.5s

Hide
Type "as alice nick alice; as mallory nick mallory" Enter
Type "echo 'correct horse battery staple' | as alice join --password-stdin meer://localhost/lobby >/dev/null" Enter
Type "echo 'guess' | as mallory join --password-stdin meer://localhost/lobby >/dev/null" Enter
Type "as alice send 'anyone up for lunch?'; as mallory send 'is this the lobby?'; clear" Enter
Show

Type "meer" Enter
Sleep 2s
Type "sure, 12:30 works" Sleep 300ms Enter
Sleep 1s
Type "/me grabs a jacket" Sleep 300ms Enter
Sleep 3s
Ctrl+D

Type "# and here's the same room, as mallory (wrong password):" Enter
Sleep 1s
Type "as mallory" Enter
Sleep 4s
Ctrl+D

Hide
Type "meer daemon stop; as alice daemon stop; as mallory daemon stop; kill %1" Enter
