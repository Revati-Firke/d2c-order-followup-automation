import assert from "node:assert/strict";
import test from "node:test";
import { excelRowsToCsv } from "./importCsv.js";

test("quotes commas and quotes in spreadsheet cells", () => {
  assert.equal(excelRowsToCsv([["A,B", 'A "quote"']]), '"A,B","A ""quote"""');
});

test("keeps date-only Excel values as date-only strings", () => {
  const dateOnly = new Date(2026, 9, 21);
  assert.equal(excelRowsToCsv([[dateOnly]]), '"2026-10-21"');
});

test("serializes timestamp Excel cells as ISO timestamps", () => {
  const timestamp = new Date("2026-10-21T08:30:00.000Z");
  assert.equal(excelRowsToCsv([[timestamp]]), '"2026-10-21T08:30:00.000Z"');
});

test("serializes empty cells and multiple rows", () => {
  assert.equal(excelRowsToCsv([["Order ID", null], ["VN-1", ""]]), '"Order ID",""\r\n"VN-1",""');
});