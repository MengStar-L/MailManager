import * as SelectPrimitive from "@radix-ui/react-select";
import { Check, ChevronDown, ChevronUp } from "lucide-react";
import type { ReactNode } from "react";

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
  leading?: ReactNode;
}

export interface SelectProps {
  value: string;
  onValueChange: (value: string) => void;
  options: SelectOption[];
  ariaLabel: string;
  id?: string;
  placeholder?: string;
  disabled?: boolean;
  size?: "default" | "compact";
  className?: string;
}

export function Select({
  value,
  onValueChange,
  options,
  ariaLabel,
  id,
  placeholder,
  disabled = false,
  size = "default",
  className = "",
}: SelectProps) {
  const selected = options.find((option) => option.value === value);

  return (
    <SelectPrimitive.Root value={value} onValueChange={onValueChange} disabled={disabled}>
      <SelectPrimitive.Trigger id={id} aria-label={ariaLabel} className={`custom-select__trigger ${className}`} data-size={size}>
        <SelectPrimitive.Value placeholder={placeholder}>
          {selected && <span className="custom-select__value">{selected.leading}<span>{selected.label}</span></span>}
        </SelectPrimitive.Value>
        <SelectPrimitive.Icon className="custom-select__icon"><ChevronDown size={14} /></SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>
      <SelectPrimitive.Portal>
        <SelectPrimitive.Content className="custom-select__content" position="popper" sideOffset={5} collisionPadding={8}>
          <SelectPrimitive.ScrollUpButton className="custom-select__scroll-button"><ChevronUp size={14} /></SelectPrimitive.ScrollUpButton>
          <SelectPrimitive.Viewport className="custom-select__viewport">
            {options.map((option) => (
              <SelectPrimitive.Item className="custom-select__item" value={option.value} disabled={option.disabled} key={option.value}>
                <SelectPrimitive.ItemIndicator className="custom-select__indicator"><Check size={14} strokeWidth={2.5} /></SelectPrimitive.ItemIndicator>
                {option.leading && <span className="custom-select__item-leading">{option.leading}</span>}
                <SelectPrimitive.ItemText className="custom-select__item-label">{option.label}</SelectPrimitive.ItemText>
              </SelectPrimitive.Item>
            ))}
          </SelectPrimitive.Viewport>
          <SelectPrimitive.ScrollDownButton className="custom-select__scroll-button"><ChevronDown size={14} /></SelectPrimitive.ScrollDownButton>
        </SelectPrimitive.Content>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  );
}
