#!/usr/bin/env python3
"""Create a 'main-sm' layout for herdr-app: copy the target machine's
main.json and insert the Serial Monitor column into work-body (next to the
Files/Changes panel). Idempotent: refuses to overwrite an existing main-sm.json,
skips insertion if a 'serial' node is already present.

Usage: patch_layout.py [LAYOUTS_DIR]   (default: ~/.config/herdr-app/layouts)
"""
import json
import os
import shutil
import sys

SERIAL_BLOCK = {
    "id": "serial",
    "type": "column",
    "size": {"px": 400},
    "min": 280,
    "max": 900,
    "children": [{"id": "serial-body", "type": "block",
                  "content": {"mod": "serial-monitor"}}],
}


def find(node, node_id):
    if node.get("id") == node_id:
        return node
    for child in node.get("children", []):
        found = find(child, node_id)
        if found is not None:
            return found
    return None


def main():
    layouts_dir = sys.argv[1] if len(sys.argv) > 1 else os.path.expanduser(
        "~/.config/herdr-app/layouts")
    src = os.path.join(layouts_dir, "main.json")
    dst = os.path.join(layouts_dir, "main-sm.json")

    if not os.path.isfile(src):
        sys.exit(f"no such layout: {src}\n"
                 "(open herdr-app once so it writes its layouts, then retry)")
    if os.path.isfile(dst):
        print(f"already installed: {dst} (left untouched)")
        return 0

    tree = json.load(open(src))
    if find(tree, "serial") is not None:
        shutil.copyfile(src, dst)
        print(f"layout already has a 'serial' block; copied as-is → {dst}")
        return 0

    body = find(tree, "work-body")
    if body is None or body.get("type") != "row":
        sys.exit("this main layout has no 'work-body' row to attach the panel to.\n"
                 "Add a block manually: layouts/<name>.json →\n"
                 f"{json.dumps(SERIAL_BLOCK, indent=2)}")

    body.setdefault("children", []).append(SERIAL_BLOCK)
    with open(dst, "w") as f:
        json.dump(tree, f, indent=2)
        f.write("\n")
    print(f"installed layout → {dst}")
    print("switch to it now (app must be running):  "
          "herdr-app do layout.switch --name main-sm")
    return 0


if __name__ == "__main__":
    sys.exit(main())
