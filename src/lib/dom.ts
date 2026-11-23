import type React from "react";

export function isDialogBackdrop(event: React.MouseEvent<HTMLDialogElement>): boolean {
  if (event.target !== event.currentTarget) return false;
  const { left, top, right, bottom } = event.currentTarget.getBoundingClientRect();
  return (
    event.clientX < left ||
    event.clientX > right ||
    event.clientY < top ||
    event.clientY > bottom
  );
}
