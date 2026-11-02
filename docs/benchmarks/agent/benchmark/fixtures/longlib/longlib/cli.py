def main(argv=None) -> int:
    """Run the longlib CLI.

    Commands:
      add <path> <key> <value>   store value (JSON when parseable, else a string)
                                 and print "ok"
      get <path> <key>           print the JSON value, or "missing" and exit 1
      list <path>                print one sorted key per line
    """
    raise NotImplementedError("main is not implemented")
