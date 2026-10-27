import { create } from "zustand";
import type { MigrationSource, ReviewModel } from "@/entities/migration-finding/model/review-model";
import { DEFAULT_RISK_SORT, type RiskSort } from "@/entities/migration-finding/model/risk-rows";
import type { ApiError, MigrationTarget, TxMode } from "@/shared/api";

export interface ReviewOptions {
  txMode: TxMode;
  assumeLockTimeout: boolean;
}

export const DEFAULT_REVIEW_OPTIONS: ReviewOptions = { txMode: "auto", assumeLockTimeout: false };

/** Problems found before anything is sent; the agent enforces the same limits. */
export type ClientProblem =
  | { code: "no_files"; ignored: string[] }
  | { code: "too_many"; count: number; limit: number }
  | { code: "too_large"; files: { name: string; bytes: number }[]; limit: number }
  | { code: "body_too_large"; bytes: number; limit: number }
  | { code: "bad_name"; names: string[] }
  | { code: "read_failed"; names: string[] }
  | { code: "no_target" }
  | { code: "cancelled" };

export type ReviewError = { kind: "client"; problem: ClientProblem } | { kind: "api"; error: ApiError };

export type ReviewStatus = "running" | "done" | "error";

/** Asks the review tab to scroll to a statement and focus its marker or the detail panel. */
export interface StatementReveal {
  index: number;
  focus: "marker" | "detail" | "none";
  nonce: number;
}

export interface MigrationReview {
  /** Also its centre tab id. */
  id: string;
  title: string;
  /** Tab scope (profile id or "sample"); the report only makes sense against that schema. */
  scope: string;
  target: MigrationTarget | null;
  /** "orders-dev" or "the sample schema". */
  targetLabel: string;
  sources: MigrationSource[];
  /** Names dropped or picked that are not migration files and were not sent. */
  ignored: string[];
  options: ReviewOptions;
  status: ReviewStatus;
  model: ReviewModel | null;
  error: ReviewError | null;
  selected: number;
  reveal: StatementReveal | null;
  startedAt: number;
}

interface ReviewState {
  reviews: Readonly<Record<string, MigrationReview>>;
  /** The review the Risk tab shows when no migration tab is active: the last one analyzed or viewed. */
  current: string | null;
  riskSort: RiskSort;
  add: (review: MigrationReview) => void;
  patch: (id: string, patch: Partial<MigrationReview>) => void;
  remove: (id: string) => void;
  select: (id: string, index: number, focus?: StatementReveal["focus"]) => void;
  setCurrent: (id: string | null) => void;
  setRiskSort: (sort: RiskSort) => void;
}

let revealNonce = 0;

export const useReviewStore = create<ReviewState>((set, get) => ({
  reviews: {},
  current: null,
  riskSort: DEFAULT_RISK_SORT,

  add: (review) => set((s) => ({ reviews: { ...s.reviews, [review.id]: review }, current: review.id })),
  patch: (id, patch) => {
    const review = get().reviews[id];
    if (!review) return;
    set((s) => ({ reviews: { ...s.reviews, [id]: { ...review, ...patch } } }));
  },
  remove: (id) => {
    const { [id]: _removed, ...rest } = get().reviews;
    set((s) => ({ reviews: rest, current: s.current === id ? null : s.current }));
  },
  select: (id, index, focus = "none") => {
    const review = get().reviews[id];
    const count = review?.model?.statements.length ?? 0;
    if (!review || count === 0) return;
    const clamped = Math.max(0, Math.min(count - 1, index));
    revealNonce += 1;
    set((s) => ({
      reviews: {
        ...s.reviews,
        [id]: { ...review, selected: clamped, reveal: { index: clamped, focus, nonce: revealNonce } },
      },
      current: id,
    }));
  },
  setCurrent: (current) => {
    if (get().current !== current) set({ current });
  },
  setRiskSort: (riskSort) => set({ riskSort }),
}));

/** Reviews in one scope, oldest first. */
export function reviewsInScope(
  reviews: Readonly<Record<string, MigrationReview>>,
  scope: string | null,
): MigrationReview[] {
  return Object.values(reviews)
    .filter((r) => r.scope === scope)
    .sort((a, b) => a.startedAt - b.startedAt);
}
