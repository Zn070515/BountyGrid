import argparse
import json

from .store import JsonStore


def main(argv=None) -> int:
    """Run the longlib CLI.

    Commands:
      add <path> <key> <value>   store value (JSON when parseable, else a string)
                                 and print "ok"
      get <path> <key>           print the JSON value, or "missing" and exit 1
      list <path>                print one sorted key per line
    """
    parser = argparse.ArgumentParser(prog="longlib")
    parser.add_argument("command", choices=["add", "get", "list"])
    parser.add_argument("path")
    parser.add_argument("key", nargs="?")
    parser.add_argument("value", nargs="?")
    args = parser.parse_args(argv)

    store = JsonStore(args.path).load()

    if args.command == "add":
        if args.key is None or args.value is None:
            parser.error("add requires <key> and <value>")
        try:
            value = json.loads(args.value)
        except json.JSONDecodeError:
            value = args.value
        store.set(args.key, value)
        store.save()
        print("ok")
        return 0

    if args.command == "get":
        if args.key is None:
            parser.error("get requires <key>")
        value = store.get(args.key)
        if value is None:
            print("missing")
            return 1
        print(json.dumps(value, sort_keys=True))
        return 0

    print("\n".join(store.keys()))
    return 0
