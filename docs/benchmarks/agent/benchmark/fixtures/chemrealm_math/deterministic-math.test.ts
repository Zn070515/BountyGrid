import { test } from "node:test";
import assert from "node:assert/strict";
import { DET_EXP10_DOMAIN, detExp10, detLog10 } from "./deterministic-math.ts";

function orderedBits(value: number): bigint {
  const buffer = new ArrayBuffer(8);
  const view = new DataView(buffer);
  view.setFloat64(0, value);
  const bits = view.getBigUint64(0);
  const sign = 0x8000000000000000n;
  return (bits & sign) === 0n ? sign + bits : sign - (bits & ~sign);
}

function ulpDistance(actual: number, expected: number): bigint {
  const actualBits = orderedBits(actual);
  const expectedBits = orderedBits(expected);
  return actualBits >= expectedBits ? actualBits - expectedBits : expectedBits - actualBits;
}

const LOG10_CASES: readonly [number, string][] = [
  [1, "0"],
  [10, "1"],
  [0.1, "-1"],
  [2, "0.301029995663981195213738894724493026768"],
  [1e-14, "-14"],
  [1e5, "5"],
  [1.00000053, "2.301760144596877955911705599375672784258e-7"],
  [1.0001, "0.0000434272768626648547275520783448479648887"],
];

const EXP10_CASES: readonly [number, string][] = [
  [-0.137, "0.7294575102545687214141455472302692669374"],
  [-0.135, "0.7328245331389040846182252915434901346266"],
  [-0.117099, "0.7636616825301398363695933656006664336279"],
  [-0.1, "0.794328234724281502065918282836387932589"],
  [-0.05, "0.891250938133745529953108681078296963985"],
  [-0.01, "0.977237220955810682697076006961561238634"],
  [0, "1"],
];

test("detLog10 matches the independent reference within one ulp", () => {
  for (const [input, expected] of LOG10_CASES) {
    assert.ok(ulpDistance(detLog10(input), Number(expected)) <= 1n, `log10(${input})`);
  }
});

test("detExp10 matches the independent reference within one ulp", () => {
  for (const [input, expected] of EXP10_CASES) {
    assert.ok(ulpDistance(detExp10(input), Number(expected)) <= 1n, `10^(${input})`);
  }
});

test("declares the measured exp10 domain explicitly", () => {
  assert.deepEqual([...DET_EXP10_DOMAIN], [-0.137, 0]);
  assert.ok(Number.isFinite(detExp10(DET_EXP10_DOMAIN[0])));
  assert.ok(Number.isFinite(detExp10(DET_EXP10_DOMAIN[1])));
});

test("rejects values outside the validated domains", () => {
  const operations: readonly (() => number)[] = [
    () => detLog10(0),
    () => detLog10(-1),
    () => detLog10(Number.MIN_VALUE),
    () => detLog10(Number.POSITIVE_INFINITY),
    () => detLog10(Number.NaN),
    () => detExp10(-0.137001),
    () => detExp10(0.000001),
    () => detExp10(Number.POSITIVE_INFINITY),
    () => detExp10(Number.NaN),
  ];
  for (const operation of operations) {
    assert.throws(operation, RangeError);
  }
});
