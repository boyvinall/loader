# VHS tape for the loader README demo.
# Install vhs: https://github.com/charmbracelet/vhs
# Regenerate with: make build && vhs demo.tape

Output demo.gif

Require ./loader

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 600
Set Theme "Monokai Vivid"
Set Padding 5
Set TypingSpeed 60ms

Type "bat -p demo.sh"
Enter
Sleep 2s

Type "./loader -r 1s -p 4 -n 10 ./demo.sh"
Sleep 3s
Enter

# The run itself takes ~16s (10 processes, 4 parallel, 5s each, 1 launch/s).
Sleep 18s

# Dashboard stays open on the final results until a key is pressed.
Sleep 1s
Space
Sleep 1s

Type q
Sleep 1s

Type "./loader -r 1s -p 4 -n 4 --no-tui ./demo.sh"
Enter

Sleep 13s