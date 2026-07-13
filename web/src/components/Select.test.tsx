import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Select } from "./Select";

const options = [
  { value: "work", label: "工作 · work@example.com", leading: <span data-testid="work-dot" /> },
  { value: "personal", label: "个人 · me@example.com", leading: <span data-testid="personal-dot" /> },
  { value: "disabled", label: "已停用", disabled: true },
];

describe("Select", () => {
  it("renders the selected value and changes it from the portal listbox", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(<Select ariaLabel="发件人" value="work" onValueChange={onValueChange} options={options} />);

    const trigger = screen.getByRole("combobox", { name: "发件人" });
    expect(trigger).toHaveTextContent("工作 · work@example.com");
    expect(screen.getByTestId("work-dot")).toBeInTheDocument();

    await user.click(trigger);
    await user.click(screen.getByRole("option", { name: /个人/ }));
    expect(onValueChange).toHaveBeenCalledWith("personal");
  });

  it("supports keyboard selection and escape", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(<Select ariaLabel="TLS" value="implicit" onValueChange={onValueChange} options={[{ value: "implicit", label: "SSL / TLS" }, { value: "starttls", label: "STARTTLS" }]} />);

    const trigger = screen.getByRole("combobox", { name: "TLS" });
    trigger.focus();
    await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");
    expect(onValueChange).toHaveBeenCalledWith("starttls");

    await user.click(trigger);
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("shows a placeholder and respects disabled states", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    const { rerender } = render(<Select ariaLabel="文件夹用途" value="" placeholder="选择用途" onValueChange={onValueChange} options={options} />);

    expect(screen.getByRole("combobox", { name: "文件夹用途" })).toHaveTextContent("选择用途");
    await user.click(screen.getByRole("combobox", { name: "文件夹用途" }));
    expect(screen.getByRole("option", { name: "已停用" })).toHaveAttribute("data-disabled");
    await user.click(screen.getByRole("option", { name: "已停用" }));
    expect(onValueChange).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");

    rerender(<Select ariaLabel="文件夹用途" value="" placeholder="选择用途" disabled onValueChange={onValueChange} options={options} />);
    expect(screen.getByRole("combobox", { name: "文件夹用途" })).toBeDisabled();
  });
});
