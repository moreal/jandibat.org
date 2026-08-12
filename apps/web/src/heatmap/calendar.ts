import type { ActivityDayDto } from "@jandibat/contracts";

export type HeatmapCell = {
  date: string;
  day?: ActivityDayDto;
  inRange: boolean;
};

export type HeatmapWeek = {
  key: string;
  cells: HeatmapCell[];
};

export type MonthLabel = {
  label: string;
  column: number;
};

export type HeatmapCalendar = {
  weeks: HeatmapWeek[];
  monthLabels: MonthLabel[];
  from: string;
  to: string;
};

const DAY_MS = 86_400_000;
const VISIBLE_DAYS = 365;
const WEEK_COLUMNS = 53;

export function parseDateKey(value: string): Date {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) throw new Error(`Invalid date key: ${value}`);
  const [, year, month, day] = match;
  const date = new Date(Date.UTC(Number(year), Number(month) - 1, Number(day)));
  if (formatDateKey(date) !== value) throw new Error(`Invalid date key: ${value}`);
  return date;
}

export function formatDateKey(date: Date): string {
  const year = date.getUTCFullYear();
  const month = String(date.getUTCMonth() + 1).padStart(2, "0");
  const day = String(date.getUTCDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function todayDateKey(now = new Date()): string {
  const year = now.getFullYear();
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function shiftDate(date: Date, days: number): Date {
  return new Date(date.getTime() + days * DAY_MS);
}

export function buildHeatmapCalendar(
  activityDays: readonly ActivityDayDto[],
  today = todayDateKey(),
): HeatmapCalendar {
  const todayDate = parseDateKey(today);
  const firstVisible = shiftDate(todayDate, -(VISIBLE_DAYS - 1));
  const currentWeekStart = shiftDate(todayDate, -todayDate.getUTCDay());
  const gridStart = shiftDate(currentWeekStart, -(WEEK_COLUMNS - 1) * 7);
  const daysByDate = new Map(activityDays.map((day) => [day.date, day]));

  const weeks = Array.from({ length: WEEK_COLUMNS }, (_, weekIndex) => {
    const weekStart = shiftDate(gridStart, weekIndex * 7);
    const cells = Array.from({ length: 7 }, (_, weekday) => {
      const date = shiftDate(weekStart, weekday);
      const dateKey = formatDateKey(date);
      return {
        date: dateKey,
        day: daysByDate.get(dateKey),
        inRange: date >= firstVisible && date <= todayDate,
      };
    });
    return { key: formatDateKey(weekStart), cells };
  });

  const monthLabels: MonthLabel[] = [];
  let previousMonth = -1;
  for (const [column, week] of weeks.entries()) {
    const representative = week.cells.find((cell) => {
      const date = parseDateKey(cell.date);
      return cell.inRange && date.getUTCDate() <= 7;
    });
    if (!representative) continue;
    const month = parseDateKey(representative.date).getUTCMonth();
    if (month === previousMonth) continue;
    monthLabels.push({
      label: new Intl.DateTimeFormat("ko-KR", {
        month: "short",
        timeZone: "UTC",
      }).format(parseDateKey(representative.date)),
      column,
    });
    previousMonth = month;
  }

  return {
    weeks,
    monthLabels,
    from: formatDateKey(firstVisible),
    to: today,
  };
}

export function heatmapSummary(
  days: readonly ActivityDayDto[],
  throughDate?: string,
): {
  total: number;
  activeDays: number;
  bestDay?: ActivityDayDto;
  currentStreak: number;
} {
  const normalized = [...days].sort((a, b) => a.date.localeCompare(b.date));
  const total = normalized.reduce((sum, day) => sum + day.count, 0);
  const activeDays = normalized.filter((day) => day.count > 0).length;
  const bestDay = normalized.reduce<ActivityDayDto | undefined>(
    (best, day) => (!best || day.count > best.count ? day : best),
    undefined,
  );

  const byDate = new Map(normalized.map((day) => [day.date, day.count]));
  let cursor = parseDateKey(throughDate ?? todayDateKey());
  if ((byDate.get(formatDateKey(cursor)) ?? 0) === 0) cursor = shiftDate(cursor, -1);
  let currentStreak = 0;
  while ((byDate.get(formatDateKey(cursor)) ?? 0) > 0) {
    currentStreak += 1;
    cursor = shiftDate(cursor, -1);
  }

  return { total, activeDays, bestDay, currentStreak };
}

export function formatActivityDate(date: string): string {
  return new Intl.DateTimeFormat("ko-KR", {
    year: "numeric",
    month: "long",
    day: "numeric",
    weekday: "short",
    timeZone: "UTC",
  }).format(parseDateKey(date));
}
