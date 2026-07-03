class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.2.2"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.2/reminal_1.2.2_darwin_arm64.tar.gz"
      sha256 "1a09bb4cb42c23cee7453cc1856226aafe367e57fa44a5e00089d8768abc96e9"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.2/reminal_1.2.2_darwin_amd64.tar.gz"
      sha256 "75c48ef39694427997fd60fd08653a42e74fa31f3b4f5c9560009421e829aa55"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.2/reminal_1.2.2_linux_arm64.tar.gz"
      sha256 "88330c77c08389d0e54d7345fdcb1206a72cc127f069c395f4e56b25fb903de6"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.2/reminal_1.2.2_linux_amd64.tar.gz"
      sha256 "662c4aa9bfb33cd41ff65b0ddb4b11e5adb2b9f07def2f2880f8277eb5343516"
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
