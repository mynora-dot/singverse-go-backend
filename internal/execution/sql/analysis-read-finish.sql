UPDATE media_analysis_read SET elapsed_ms=elapsed_ms-@{refund},updated_at=@{now} WHERE id=@{id} AND elapsed_ms>=@{refund}
