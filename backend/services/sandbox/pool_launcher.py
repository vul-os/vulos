"""
Vulos OS Sandbox Pool Launcher
Trusted bootstrap script — NOT user code.

Reads a single line from stdin: the path to the user script.
Then exec()s that script in the current process, inheriting
VULOS_PORT / PORT from the environment already set by the Go host.

This file is written by the Go backend and is not user-supplied.
"""
import os
import sys

def main():
    line = sys.stdin.readline().rstrip("\n")
    if not line:
        sys.exit(1)

    script_path = line
    if not os.path.isfile(script_path):
        sys.stderr.write(f"pool_launcher: script not found: {script_path}\n")
        sys.exit(1)

    with open(script_path, "r") as f:
        source = f.read()

    # Compile and execute the user script in a fresh globals dict that
    # mimics __main__ so relative imports and __file__ work as expected.
    g = {
        "__name__": "__main__",
        "__file__": script_path,
        "__builtins__": __builtins__,
    }
    code = compile(source, script_path, "exec")
    exec(code, g)

if __name__ == "__main__":
    main()
