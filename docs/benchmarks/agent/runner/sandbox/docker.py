# -*- coding: utf-8 -*-
import dataclasses


@dataclasses.dataclass
class ExecResult:
    exit_code: int | None
    stdout: str = ""
    stderr: str = ""
    timed_out: bool = False
