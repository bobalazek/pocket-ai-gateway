import type { WeeklyPriceWindow } from "@/features/usage/types/usage.types";

export const weekdayOptions = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"] as const;

const minutesPerDay = 24 * 60;
const minutesPerWeek = 7 * minutesPerDay;

function weeklyMinute(day: string, time: string, endpoint: "start" | "end") {
  if (!day && !time) return null;
  if (!day || !time) throw new Error(`Choose both a day and time for the weekly ${endpoint}.`);

  const dayIndex = Number(day);
  const match = /^(\d{2}):(\d{2})$/.exec(time);
  if (!Number.isInteger(dayIndex) || dayIndex < 0 || dayIndex > (endpoint === "end" ? 7 : 6) || !match) {
    throw new Error(`Choose a valid weekly ${endpoint}.`);
  }
  const hour = Number(match[1]);
  const minute = Number(match[2]);
  if (hour > 23 || minute > 59 || (dayIndex === 7 && (hour !== 0 || minute !== 0))) {
    throw new Error(`Choose a valid weekly ${endpoint}.`);
  }
  return dayIndex * minutesPerDay + hour * 60 + minute;
}

export function parseWeeklyPriceWindow(startDay: string, startTime: string, endDay: string, endTime: string): WeeklyPriceWindow {
  const weekly_start_minute_utc = weeklyMinute(startDay, startTime, "start");
  const weekly_end_minute_utc = weeklyMinute(endDay, endTime, "end");
  if (weekly_start_minute_utc === null && weekly_end_minute_utc === null) {
    return { weekly_start_minute_utc: null, weekly_end_minute_utc: null };
  }
  if (weekly_start_minute_utc === null || weekly_end_minute_utc === null) {
    throw new Error("Choose both a start and end for the weekly price window.");
  }
  if (weekly_start_minute_utc >= weekly_end_minute_utc) {
    throw new Error("The weekly price window must end after it starts.");
  }
  return { weekly_start_minute_utc, weekly_end_minute_utc };
}

function formatWeeklyMinute(value: number) {
  if (value === minutesPerWeek) return "Monday 00:00 next week";
  const day = weekdayOptions[Math.floor(value / minutesPerDay)];
  const minuteOfDay = value % minutesPerDay;
  const hour = Math.floor(minuteOfDay / 60).toString().padStart(2, "0");
  const minute = (minuteOfDay % 60).toString().padStart(2, "0");
  return `${day} ${hour}:${minute}`;
}

export function formatWeeklyPriceWindow(start: number | null, end: number | null) {
  if (start === null && end === null) return "All week";
  if (start === null || end === null || start < 0 || end > minutesPerWeek || start >= end) return "Weekly window unavailable";
  return `${formatWeeklyMinute(start)}–${formatWeeklyMinute(end)} UTC`;
}
