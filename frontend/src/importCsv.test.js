import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { readSheet } from "read-excel-file/node";
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

test("committed XLSX sample includes valid cases for every exception rule", async () => {
  const workbookPath = new URL("../../backend/sample_data/d2c_order_followup_sample_109.xlsx", import.meta.url);
  const rows = await readSheet(await readFile(workbookPath));
  assert.equal(rows.length, 110);
  const samples = new Map(rows.slice(1).map((row) => [row[0], row]));
  assert.equal(samples.get("EXC-UNCONFIRMED")?.[7], "Pending");
  assert.equal(samples.get("EXC-NOT-DISPATCHED-30H")?.[7], "Pending");
  assert.equal(samples.get("EXC-18H-THRESHOLD")?.[7], "Pending");
  assert.equal(samples.get("EXC-HIGH-VALUE-LATE")?.[6], "Undelivered");
  assert.equal(samples.get("ERR-MISSING-CUSTOMER")?.[2], null);
  assert.equal(samples.get("ERR-INVALID-DATE")?.[1], "not-a-date");
  assert.equal(samples.get("ORD-001")?.[0], "ORD-001");
});