import { screen } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { afterEach, describe, expect, it } from "vitest";
import { useActiveConnectionStore } from "@/entities/connection";
import { buildSchemaGraph, closeSampleWorkspace } from "@/entities/schema";
import { InferredFkActions } from "@/features/review-inferred-fk";
import { type Model, queryKeys, schemaQueryKeys } from "@/shared/api";
import { sampleModel } from "@/test/fixtures";
import { makeProfile, renderWithProviders } from "@/test/render";
import { server } from "@/test/server";

const profile = makeProfile();
const KEY = "app.events(user_id)->app.users(id)";

afterEach(() => {
  closeSampleWorkspace();
  useActiveConnectionStore.getState().setActiveId(null);
});

function setup() {
  useActiveConnectionStore.getState().setActiveId(profile.id);
  const model = sampleModel();
  const edge = buildSchemaGraph(model).edges.find((e) => e.fk.name === KEY);
  if (!edge) throw new Error("fixture changed");
  const view = renderWithProviders(<InferredFkActions edge={edge} />);
  view.queryClient.setQueryData(queryKeys.profiles, [profile]);
  view.queryClient.setQueryData(schemaQueryKeys.schema(profile.id), model);
  const cached = () => view.queryClient.getQueryData<Model>(schemaQueryKeys.schema(profile.id));
  const eventsFks = () => cached()?.relations?.find((r) => r.name === "events")?.foreignKeys ?? [];
  return { ...view, cached, eventsFks };
}

describe("InferredFkActions", () => {
  it("shows the confidence and accepting saves the decision and marks the key accepted", async () => {
    const bodies: unknown[] = [];
    server.use(
      http.get("/api/profiles", () => HttpResponse.json([profile])),
      http.put(`/api/profiles/${profile.id}/inferred-fks`, async ({ request }) => {
        bodies.push(await request.json());
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const { user, eventsFks } = setup();
    expect(screen.getByText("80%")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Accept" }));
    expect(bodies).toEqual([{ key: KEY, status: "accepted" }]);
    expect(eventsFks().find((fk) => fk.name === KEY)?.confidence).toBe(1);
    expect(await screen.findByTestId("live-region")).toHaveTextContent(`Accepted the inferred key ${KEY}.`);
  });

  it("rejecting removes the key from the cached schema", async () => {
    const bodies: unknown[] = [];
    server.use(
      http.get("/api/profiles", () => HttpResponse.json([profile])),
      http.put(`/api/profiles/${profile.id}/inferred-fks`, async ({ request }) => {
        bodies.push(await request.json());
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const { user, eventsFks } = setup();
    await user.click(screen.getByRole("button", { name: "Reject" }));
    expect(bodies).toEqual([{ key: KEY, status: "rejected" }]);
    expect(eventsFks().map((fk) => fk.name)).toEqual(["events_team_id_fkey"]);
  });

  it("puts the key back and says so when the save fails", async () => {
    server.use(
      http.get("/api/profiles", () => HttpResponse.json([profile])),
      http.put(`/api/profiles/${profile.id}/inferred-fks`, () =>
        HttpResponse.json({ error: { code: "internal", message: "disk full" } }, { status: 500 }),
      ),
    );
    const { user, eventsFks } = setup();
    await user.click(screen.getByRole("button", { name: "Reject" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not save the decision: disk full");
    expect(eventsFks().map((fk) => fk.name)).toEqual(["events_team_id_fkey", KEY]);
  });
});
