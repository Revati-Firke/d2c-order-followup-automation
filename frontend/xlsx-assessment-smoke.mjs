import { readFile } from "node:fs/promises";
import { readSheet } from "read-excel-file/node";
import { excelRowsToCsv } from "./src/importCsv.js";

const workbook = await readSheet(await readFile(process.argv[2]));
const form = new FormData();
form.append("file", new File([excelRowsToCsv(workbook)], "converted.csv", { type: "text/csv" }));
form.append("filename", "d2c_order_followup_sample_109.xlsx");
form.append("actor", "Final audit");
const response = await fetch("http://localhost:3002/api/import", { method: "POST", body: form });
const { import: record } = await response.json();
console.log(JSON.stringify({ status: response.status, accepted: record?.acceptedRows, invalid: record?.invalidRows?.length, received: record?.receivedRows, filename: record?.filename }));
if (!response.ok || record.acceptedRows !== 106 || record.invalidRows.length !== 3 || record.receivedRows !== 109) process.exit(1);
