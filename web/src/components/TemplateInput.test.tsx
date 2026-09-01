import { useState } from "react";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TemplateInput } from "./TemplateInput";

function Harness({ initial, tracks }: { initial: string; tracks?: boolean }) {
  const [value, setValue] = useState(initial);
  return (
    <TemplateInput
      label="Single-file template"
      value={value}
      onChange={setValue}
      includeTrackTokens={tracks}
    />
  );
}

describe("TemplateInput token palette", () => {
  it("inserts a token at the caret, not at the end", async () => {
    const user = userEvent.setup();
    render(<Harness initial="{title} - {ext}" />);

    const field = screen.getByRole("textbox", {
      name: /single-file template/i,
    }) as HTMLInputElement;
    await user.click(screen.getByRole("button", { name: /insert token/i }));

    field.focus();
    field.setSelectionRange(10, 10); // between "- " and "{ext}"
    await user.click(screen.getByRole("button", { name: "{author}" }));

    expect(field.value).toBe("{title} - {author}{ext}");
    expect(field.selectionStart).toBe(18);
  });

  it("replaces the selection when a token is inserted", async () => {
    const user = userEvent.setup();
    render(<Harness initial="{title}{ext}" />);

    const field = screen.getByRole("textbox", {
      name: /single-file template/i,
    }) as HTMLInputElement;
    await user.click(screen.getByRole("button", { name: /insert token/i }));

    field.focus();
    field.setSelectionRange(0, 7); // "{title}"
    await user.click(screen.getByRole("button", { name: "{series}" }));

    expect(field.value).toBe("{series}{ext}");
  });

  it("wraps the selection in an optional group", async () => {
    const user = userEvent.setup();
    render(<Harness initial="{title} ({year}){ext}" />);

    const field = screen.getByRole("textbox", {
      name: /single-file template/i,
    }) as HTMLInputElement;
    await user.click(screen.getByRole("button", { name: /insert token/i }));

    field.focus();
    field.setSelectionRange(7, 16); // " ({year})"
    await user.click(screen.getByRole("button", { name: /\[ … \]/ }));

    expect(field.value).toBe("{title}[ ({year})]{ext}");
  });

  it("offers track tokens only for the multi-file template", async () => {
    const user = userEvent.setup();
    const { unmount } = render(<Harness initial="" />);
    await user.click(screen.getByRole("button", { name: /insert token/i }));
    expect(screen.queryByRole("button", { name: "{track2}" })).toBeNull();
    unmount();

    render(<Harness initial="" tracks />);
    await user.click(screen.getByRole("button", { name: /insert token/i }));
    expect(screen.getByRole("button", { name: "{track2}" })).toBeTruthy();
  });
});
