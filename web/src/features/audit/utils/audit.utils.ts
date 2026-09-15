export function canonicalTime(value: string) {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|[+-](\d{2}):(\d{2}))$/.exec(value);
  if (!match) return "";
  const [year, month, day, hour, minute, second, offsetHour, offsetMinute] = [match[1], match[2], match[3], match[4], match[5], match[6], match[8] ?? "0", match[9] ?? "0"].map(Number);
  if (month < 1 || month > 12 || day < 1 || day > new Date(Date.UTC(year, month, 0)).getUTCDate() || hour > 23 || minute > 59 || second > 59 || offsetHour > 23 || offsetMinute > 59) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : date.toISOString();
}

export function localTime(value?: string) {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

export function prettyJSON(value: string) { try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return value; } }
