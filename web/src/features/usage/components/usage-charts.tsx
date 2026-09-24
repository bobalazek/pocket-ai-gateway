import { Area, AreaChart, Bar, BarChart, CartesianGrid, Legend, Tooltip, XAxis, YAxis } from "recharts";

import { ChartContainer } from "@/components/ui/chart";
import type { UsagePoint } from "@/features/usage/types/usage.types";

const dateLabel = (value: string) => value.slice(5);

export function RequestTrendChart({ points }: { points: UsagePoint[] }) {
  return <ChartContainer label="Daily gateway requests">
    <BarChart responsive width="100%" height="100%" data={points}>
      <CartesianGrid vertical={false} />
      <XAxis dataKey="date" tickFormatter={dateLabel} />
      <YAxis width={38} allowDecimals={false} />
      <Tooltip />
      <Bar dataKey="requests" name="Requests" fill="var(--accent)" radius={[4, 4, 0, 0]} isAnimationActive={false} />
    </BarChart>
  </ChartContainer>;
}

export function TokenTrendChart({ points }: { points: UsagePoint[] }) {
  return <ChartContainer label="Daily input and output tokens">
    <AreaChart responsive width="100%" height="100%" data={points}>
      <CartesianGrid vertical={false} />
      <XAxis dataKey="date" tickFormatter={dateLabel} />
      <YAxis width={56} />
      <Tooltip />
      <Legend />
      <Area type="monotone" dataKey="input_tokens" name="Input" stackId="tokens" stroke="var(--accent)" fill="var(--accent-soft)" isAnimationActive={false} />
      <Area type="monotone" dataKey="output_tokens" name="Output" stackId="tokens" stroke="var(--text)" fill="var(--chart-ink-soft)" isAnimationActive={false} />
    </AreaChart>
  </ChartContainer>;
}

export function CostTrendChart({ points }: { points: UsagePoint[] }) {
  const data = points.map((point) => ({ ...point, cost: Number(point.known_cost_usd) }));
  return <ChartContainer label="Daily known spend in US dollars">
    <BarChart responsive width="100%" height="100%" data={data}>
      <CartesianGrid vertical={false} />
      <XAxis dataKey="date" tickFormatter={dateLabel} />
      <YAxis width={48} tickFormatter={(value: number) => `$${value.toFixed(2)}`} />
      <Tooltip formatter={(value) => `$${Number(value).toFixed(4)}`} />
      <Bar dataKey="cost" name="Known spend" fill="var(--text)" radius={[4, 4, 0, 0]} isAnimationActive={false} />
    </BarChart>
  </ChartContainer>;
}
