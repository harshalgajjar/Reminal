class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.7.2"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.2/reminal_1.7.2_darwin_arm64.tar.gz"
      sha256 "a4c625aaf8dface8e97732c69b4cef6fad51f546dece08d48451e94c0761c7ae"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.2/reminal_1.7.2_darwin_amd64.tar.gz"
      sha256 "5759a7ecfc3da5271e0cba60b7cdb880ef6c05414ad299110d5b5cd2fb87fa50"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.2/reminal_1.7.2_linux_arm64.tar.gz"
      sha256 "9e14e66bb39722a40f7fccd4d6e9bac5f55e7dc88312dc148ce2047e71036865"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.2/reminal_1.7.2_linux_amd64.tar.gz"
      sha256 "e6080afab2b46fb642ccf1fad5e1be885838d8a7f59825d54cbbb177d092abc8"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
