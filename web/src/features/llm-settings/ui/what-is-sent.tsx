import { ShieldCheck } from "lucide-react";
import { useActiveConnection } from "@/entities/connection";
import { destination, type LlmFormValues } from "@/features/llm-settings/model/form";
import type { Profile } from "@/shared/api";

function resultsLine(profile: Profile): string {
  switch (profile.summaryMode) {
    case "off":
      return `For ${profile.name}: no query results, only the schema.`;
    case "profile":
      return `For ${profile.name}: only the schema. dbscope counts the result summary itself, so no result data is sent.`;
    case "rows":
      return `For ${profile.name}: up to ${profile.maxRowsToLLM} result rows${profile.env === "prod-replica" ? ", after you confirm once, since this is a prod-replica" : ""}.`;
  }
}

/** Plain-language statement of what leaves the machine (UX rule 14). */
export function WhatIsSent({ values }: { values: Pick<LlmFormValues, "provider" | "baseUrl"> }) {
  const { profile } = useActiveConnection();
  if (values.provider === "none") {
    return (
      <p className="rounded-md border border-border-default bg-surface-subtle p-3 text-xs text-text-muted">
        With no provider the assistant is off and nothing is sent anywhere.
      </p>
    );
  }
  return (
    <section
      aria-labelledby="llm-what-is-sent"
      className="flex flex-col gap-1.5 rounded-md border border-border-default bg-surface-subtle p-3 text-xs"
    >
      <h3 id="llm-what-is-sent" className="flex items-center gap-1.5 font-semibold text-text-primary">
        <ShieldCheck aria-hidden className="size-3.5 text-accent-fg" />
        What is sent to {destination(values)}
      </h3>
      <ul className="flex list-disc flex-col gap-1 pl-4 text-text-muted">
        <li>
          Your question and the schema of the relevant tables: names, columns, types, keys, comments and your glossary.
          Columns that look sensitive (password, token, secret, API key, card number and similar) are left out.
        </li>
        <li>
          Query results depend on each connection's setting.{" "}
          {profile ? resultsLine(profile) : "Off sends none, Profile sends statistics only, Rows sends sample rows."}
        </li>
        <li>The Test button sends one fixed test prompt, never schema or data.</li>
      </ul>
      <p className="text-text-muted">
        Every answer names the model and lists what was sent for it. The API key stays in dbscope's encrypted vault on
        this machine.
      </p>
    </section>
  );
}
