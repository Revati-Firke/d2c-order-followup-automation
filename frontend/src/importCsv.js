function csvCell(value) {
  let text;
  if (value instanceof Date) {
    text = value.getHours() === 0 && value.getMinutes() === 0 && value.getSeconds() === 0
      ? `${value.getFullYear()}-${String(value.getMonth() + 1).padStart(2, "0")}-${String(value.getDate()).padStart(2, "0")}`
      : value.toISOString();
  } else {
    text = String(value ?? "");
  }
  return `"${text.replaceAll('"', '""')}"`;
}

export function excelRowsToCsv(rows) {
  return rows.map((row) => row.map(csvCell).join(",")).join("\r\n");
}